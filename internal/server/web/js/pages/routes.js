// Routes: one table row per route, "ingress port → egress port". Rows are
// edited in place; the new-route row at the bottom adds one. Several ingress
// ports may feed the same egress port, but every ingress port has exactly one
// destination. The map below the table shows the same picture and lets the
// operator arrange it.
import { html, useState } from "../lib/html.js";
import { api } from "../lib/api.js";
import { useMapLayout } from "../lib/maplayout.js";
import { navigate } from "../lib/router.js";
import { ingressState, mutate, portName, useApp } from "../state.js";
import { Icon } from "../ui/icons.js";
import { RouteMap } from "../charts/routemap.js";
import { Button, Card, Empty, IconButton, PageHeader, Select, Status } from "../ui/kit.js";

const CHOOSE = "";

function ingressOptions(ports, status) {
  return ports.map((p) => ({
    value: String(p.id),
    label: `${portName(status, p.id)}${p.label ? ` — ${p.label}` : ""}`,
  }));
}

function endpointOptions(status) {
  return status.endpoints.map((e) => ({
    value: String(e.id),
    label: `${e.display}${e.label ? ` — ${e.label}` : ""}`,
  }));
}

export function RoutesPage() {
  const { status } = useApp();
  const { layout, update } = useMapLayout();
  const missing = !status.inports.length || !status.endpoints.length;

  // sorting by egress port keeps the ingress ports that feed it together
  const endpointPos = new Map(status.endpoints.map((e, i) => [e.id, i]));
  const rows = status.routes
    .map((r) => ({ route: r, port: status.inports.find((p) => p.id === r.inport_id) }))
    .filter((x) => x.port)
    .sort((a, b) => endpointPos.get(a.route.endpoint_id) - endpointPos.get(b.route.endpoint_id)
      || a.port.machine_label.localeCompare(b.port.machine_label) || a.port.port - b.port.port);
  const unrouted = status.inports.filter((p) => !p.route_id);

  return html`
    <${PageHeader} title="Routes"
      description="Each row sends one ingress port to one egress port. Several ingress ports can feed the same egress port." />

    <${Card} flush>
      ${missing ? html`<${Empty} icon="route" title=${status.endpoints.length ? "No ingress ports yet" : "No egress ports yet"}
          action=${html`<${Button} variant="primary" icon="plus"
            onClick=${() => navigate(status.endpoints.length ? "ingress" : "egress")}>${status.endpoints.length ? "Add ingress port" : "Add egress port"}<//>`}>
          A route connects an ingress port to an egress port, so you need at least one of each.
        <//>` : html`<div class="table-wrap"><table class="routes-table">
        <thead><tr><th class="toggle-col"></th><th>Ingress port</th><th class="arrow-col"></th><th>Egress port</th><th>Status</th><th class="actions-col"></th></tr></thead>
        <tbody>
          ${rows.map(({ route, port }) => html`<${RouteRow} key=${route.id} route=${route} port=${port} status=${status} unrouted=${unrouted} />`)}
          <${NewRouteRow} status=${status} unrouted=${unrouted} />
        </tbody>
      </table></div>`}
    <//>

    <${Card} title="Map">
      <${RouteMap} status=${status} layout=${layout} onChange=${update} editable />
    <//>`;
}

function RouteRow({ route, port, status, unrouted }) {
  const st = ingressState(port);
  const name = portName(status, port.id);
  // an ingress port can be swapped for one that has no route yet
  const ingress = ingressOptions([port, ...unrouted], status);
  const change = (patch, text) => mutate(() => api.updateRoute(route.id, patch), text);
  const remove = () => mutate(() => api.deleteRoute(route.id), `Route from ${name} deleted`);
  const on = route.enabled;
  const toggle = () => change({ enabled: !on }, on ? `Route from ${name} switched off` : `Route from ${name} switched on`);

  return html`<tr class=${`${port.bind_error ? "row-critical" : ""}${on ? "" : " route-off"}`}>
    <td class="toggle-col"><button type="button" class=${`route-toggle ${on ? "on" : "off"}`} onClick=${toggle}
      role="switch" aria-checked=${on} aria-label=${`Route from ${name}`}
      title=${on ? "Route is on — click to switch it off" : "Route is off — click to switch it on"}><${Icon} name="power" /></button></td>
    <td class="select-cell"><${Select} value=${String(port.id)} options=${ingress} aria-label="Ingress port"
      onChange=${(v) => change({ inport_id: Number(v) }, "Route updated")} /></td>
    <td class="arrow-col"><${Icon} name="arrow" /></td>
    <td class="select-cell"><${Select} value=${String(route.endpoint_id)} options=${endpointOptions(status)} aria-label="Egress port"
      onChange=${(v) => change({ endpoint_id: Number(v) }, "Route updated")} /></td>
    <td><${Status} tone=${st.tone} title=${port.bind_error}>${st.text}<//></td>
    <td class="actions-col"><${IconButton} icon="trash" label=${`Delete route from ${name}`} onClick=${remove} /></td>
  </tr>`;
}

// NewRouteRow adds a route for an ingress port that has none yet.
function NewRouteRow({ status, unrouted }) {
  const [inport, setInport] = useState(CHOOSE);
  const [endpoint, setEndpoint] = useState(CHOOSE);
  const [busy, setBusy] = useState(false);
  const ready = inport !== CHOOSE && endpoint !== CHOOSE;
  const noPorts = unrouted.length === 0;

  const add = async () => {
    setBusy(true);
    const ok = await mutate(() => api.createRoute(Number(inport), Number(endpoint)), "Route added");
    setBusy(false);
    if (ok) { setInport(CHOOSE); setEndpoint(CHOOSE); }
  };

  return html`<tr class="new-route">
    <td class="toggle-col"></td>
    <td class="select-cell"><${Select} value=${inport} aria-label="New route: ingress port" disabled=${noPorts}
      onChange=${setInport}
      options=${[{ value: CHOOSE, label: noPorts ? "every ingress port is routed" : "Choose ingress port…" },
        ...ingressOptions(unrouted, status)]} /></td>
    <td class="arrow-col"><${Icon} name="arrow" /></td>
    <td class="select-cell"><${Select} value=${endpoint} aria-label="New route: egress port" onChange=${setEndpoint}
      options=${[{ value: CHOOSE, label: "Choose egress port…" }, ...endpointOptions(status)]} /></td>
    <td colspan="2" class="new-route-action"><${Button} variant="primary" icon="plus" busy=${busy}
      disabled=${!ready} onClick=${add}>Add route<//></td>
  </tr>`;
}
