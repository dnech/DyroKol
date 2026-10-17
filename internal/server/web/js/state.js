// Application state: authentication, the relay status snapshot (polled) and
// the static relay config. Pages read it with useApp(); mutations go through
// mutate() so every change refreshes the snapshot and reports the outcome.
import { api, auth } from "./lib/api.js";
import { createStore, useStore } from "./lib/store.js";
import { useState } from "./lib/html.js";
import { toast } from "./ui/toast.js";
import { ago } from "./lib/format.js";

const POLL_MS = 5000;

export const app = createStore({
  authed: !!auth.token,
  status: null,      // GET /api/status
  config: null,      // GET /api/config
  error: null,       // last refresh error
  updatedAt: 0,      // ms of the last successful refresh
  rates: {},         // per ingress port: smoothed traffic, see trackRates()
});

export const useApp = () => useStore(app);

let timer = 0;

// --- traffic rates (drive the animation on the route map) ---
//
// Rates come from the byte counters of two consecutive snapshots, smoothed with
// a moving average. "in" is visitor → target, "out" is target → visitor. When a
// direction goes quiet its level decays step by step instead of dropping to
// zero, so the dots fade out while still moving.

const SMOOTH = 0.5;      // weight of the newest sample in the moving average
const FADE = 0.2;        // level kept per quiet snapshot (gone in about two polls)
const MIN_WINDOW_S = 2;  // ignore snapshots closer together than this
let prevSample = null;   // { t, bytes: Map<inport id, [in, out]> }

function trackDir(prev, delta, dt) {
  const rate = delta / dt;
  if (delta > 0) return { rate: prev ? prev.rate * (1 - SMOOTH) + rate * SMOOTH : rate, level: 1 };
  const level = prev ? prev.level * FADE : 0;
  return level < 0.05 ? null : { rate: prev.rate * (1 - SMOOTH), level };
}

function trackRates(status, old) {
  const t = performance.now();
  const bytes = new Map(status.inports.map((p) => [p.id, [p.bytes_in || 0, p.bytes_out || 0]]));
  if (!prevSample) { prevSample = { t, bytes }; return old; }
  const dt = (t - prevSample.t) / 1000;
  if (dt < MIN_WINDOW_S) return old;
  const next = {};
  for (const [id, [i, o]] of bytes) {
    const before = prevSample.bytes.get(id) || [i, o];
    const prev = old[id] || {};
    const rin = trackDir(prev.in, Math.max(0, i - before[0]), dt);
    const rout = trackDir(prev.out, Math.max(0, o - before[1]), dt);
    if (rin || rout) next[id] = { in: rin, out: rout };
  }
  prevSample = { t, bytes };
  return next;
}

export async function refresh() {
  try {
    const status = await api.status();
    app.set({ status, error: null, updatedAt: Date.now(), rates: trackRates(status, app.get().rates) });
  } catch (error) {
    if (error.status !== 401) app.set({ error });
  }
}

async function loadConfig() {
  try {
    app.set({ config: await api.config() });
  } catch { /* optional */ }
}

function schedule() {
  clearTimeout(timer);
  timer = setTimeout(async () => {
    if (!document.hidden && app.get().authed) await refresh();
    schedule();
  }, POLL_MS);
}

document.addEventListener("visibilitychange", () => {
  if (!document.hidden && app.get().authed) refresh();
});

auth.onUnauthorized(() => {
  app.set({ authed: false, status: null });
  clearTimeout(timer);
});

// login validates the token against the relay before keeping it.
export async function login(token) {
  auth.set(token);
  try {
    const status = await api.status();
    app.set({ authed: true, status, error: null, updatedAt: Date.now() });
    loadConfig();
    schedule();
  } catch (error) {
    auth.set("");
    throw error;
  }
}

export function logout() {
  auth.set("");
  clearTimeout(timer);
  app.set({ authed: false, status: null, config: null });
}

export function start() {
  if (!auth.token) return;
  refresh().then(() => { if (app.get().authed) { loadConfig(); schedule(); } });
}

// mutate runs an API change, refreshes the snapshot and reports the result
// as a toast. It resolves to the call's result, or undefined on failure.
export async function mutate(call, success) {
  try {
    const result = await call();
    if (success) toast.success(success);
    await refresh();
    return result;
  } catch (error) {
    if (error.status !== 401) toast.error(error.message);
    return undefined;
  }
}

// useAction drives a form submit: busy flag, inline error, and on success a
// snapshot refresh, an optional toast and onDone(result).
export function useAction(call, { success, onDone } = {}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const run = async (...args) => {
    setBusy(true);
    setError("");
    try {
      const result = await call(...args);
      setBusy(false);
      if (success) toast.success(typeof success === "function" ? success(result) : success);
      refresh();
      onDone?.(result);
      return result;
    } catch (e) {
      setBusy(false);
      setError(e.message);
      return undefined;
    }
  };
  return { busy, error, run };
}

// --- derived views of the status snapshot ---

export function machineName(status, id) {
  if (!status || id === status.relay_label) return "relay";
  const c = status.clients.find((x) => x.key_hash === id);
  return c ? c.label || c.prefix : String(id).slice(0, 12);
}

export function machineOptions(status) {
  const out = [{ value: status.relay_label, label: "relay (this server)" }];
  for (const c of status.clients) {
    out.push({ value: c.key_hash, label: `${c.label || c.prefix}${c.online ? "" : " · offline"}` });
  }
  return out;
}

// portName renders an ingress port as machine:port. Port numbers are only
// unique per machine, so a bare ":8080" would be ambiguous.
export function portName(status, inportId) {
  const p = status?.inports.find((x) => x.id === inportId);
  return p ? `${p.machine_label}:${p.port}` : `#${inportId}`;
}

// issues lists what needs the operator's attention.
export function issues(status) {
  if (!status) return [];
  const out = [];
  for (const p of status.inports) {
    const name = `${p.machine_label}:${p.port}${p.label ? ` (${p.label})` : ""}`;
    if (p.bind_error) out.push({ level: "critical", page: "ingress", text: `Ingress ${name} cannot bind`, detail: p.bind_error });
    else if (!p.route_id) out.push({ level: "warning", page: "routes", text: `Ingress ${name} is not routed`, detail: "traffic to this port is dropped" });
    else if (!p.endpoint_online) out.push({ level: "serious", page: "routes", text: `Ingress ${name}: target machine offline`, detail: p.target });
  }
  for (const c of status.clients) {
    if (!c.online && (c.inport_count || c.endpoint_count)) {
      out.push({ level: "serious", page: "machines", text: `Machine ${c.label || c.prefix} is offline`, detail: `last seen ${ago(c.last_seen)}` });
    }
  }
  return out;
}

// ingressState summarizes an ingress port for display: a status tone, its
// label, and which kind of line it draws on the route map.
export function ingressState(p) {
  if (p.bind_error) return { tone: "critical", text: "bind failed", line: "crit" };
  if (!p.route_id) return { tone: "warning", text: "not routed" };
  if (!p.route_enabled) return { tone: "offline", text: "route off", line: "off" };
  if (!p.endpoint_online) return { tone: "serious", text: "target offline", line: "warn" };
  return { tone: "good", text: p.active ? `${p.active} active` : "forwarding", line: "ok" };
}
