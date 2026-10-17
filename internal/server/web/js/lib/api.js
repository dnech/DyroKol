// Admin API client. Every call carries the token in X-Dyrokol-Api; a 401
// clears the token and notifies the app so it can show the login screen.

const TOKEN_KEY = "dyrokol_token";
const HEADER = "X-Dyrokol-Api";

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

let token = sessionStorage.getItem(TOKEN_KEY) || "";
const unauthorizedListeners = new Set();

export const auth = {
  get token() { return token; },
  set(t) {
    token = t || "";
    if (token) sessionStorage.setItem(TOKEN_KEY, token);
    else sessionStorage.removeItem(TOKEN_KEY);
  },
  onUnauthorized(fn) {
    unauthorizedListeners.add(fn);
    return () => unauthorizedListeners.delete(fn);
  },
};

async function request(method, path, body, { raw = false } = {}) {
  const headers = { [HEADER]: token };
  let payload;
  if (body instanceof Blob || typeof body === "string") {
    payload = body;
    headers["Content-Type"] = "application/json";
  } else if (body !== undefined) {
    payload = JSON.stringify(body);
    headers["Content-Type"] = "application/json";
  }
  let res;
  try {
    res = await fetch(path, { method, headers, body: payload });
  } catch {
    throw new ApiError(0, "relay unreachable");
  }
  if (res.status === 401) {
    auth.set("");
    unauthorizedListeners.forEach((fn) => fn());
    throw new ApiError(401, "invalid or missing token");
  }
  if (raw && res.ok) return res;
  const data = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(res.status, data?.error || res.statusText);
  return data;
}

const enc = encodeURIComponent;

export const api = {
  config: () => request("GET", "/api/config"),
  status: () => request("GET", "/api/status"),
  stats: (since) => request("GET", `/api/stats?since=${since}`),
  peers: (limit = 200) => request("GET", `/api/peers?limit=${limit}`),
  live: () => request("GET", "/api/live"),
  logs: (limit = 500) => request("GET", `/api/logs?limit=${limit}`),

  createClient: (body) => request("POST", "/api/clients", body),
  updateClient: (hash, patch) => request("PATCH", `/api/clients/${enc(hash)}`, patch),
  regenerateClient: (hash) => request("POST", `/api/clients/${enc(hash)}/regenerate`),
  deleteClient: (hash) => request("DELETE", `/api/clients/${enc(hash)}`),

  createInPort: (body) => request("POST", "/api/inports", body),
  updateInPort: (id, patch) => request("PATCH", `/api/inports/${id}`, patch),
  deleteInPort: (id) => request("DELETE", `/api/inports/${id}`),

  createEndpoint: (body) => request("POST", "/api/endpoints", body),
  updateEndpoint: (id, patch) => request("PATCH", `/api/endpoints/${id}`, patch),
  deleteEndpoint: (id) => request("DELETE", `/api/endpoints/${id}`),

  createRoute: (inportId, endpointId) =>
    request("POST", "/api/routes", { inport_id: inportId, endpoint_id: endpointId }),
  updateRoute: (id, patch) => request("PATCH", `/api/routes/${id}`, patch),
  deleteRoute: (id) => request("DELETE", `/api/routes/${id}`),
  // setEndpointRoutes makes inportIds exactly the ingress ports routed to the endpoint.
  setEndpointRoutes: (endpointId, inportIds) =>
    request("PUT", `/api/endpoints/${endpointId}/routes`, { inport_ids: inportIds }),

  getMap: () => request("GET", "/api/map"),
  saveMap: (layout) => request("PUT", "/api/map", layout),

  backup: (secrets) => request("GET", `/api/backup?secrets=${secrets ? 1 : 0}`, undefined, { raw: true }),
  restore: (text) => request("POST", "/api/restore", text),
  saveSettings: (settings) => request("PUT", "/api/settings", settings),
  shutdown: () => request("POST", "/api/shutdown"),
};
