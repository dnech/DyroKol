// Activity: connections flowing right now and remote addresses seen recently.
import { html, useState } from "../lib/html.js";
import { api } from "../lib/api.js";
import { usePolled } from "../lib/store.js";
import { ago, bytes, duration } from "../lib/format.js";
import { useApp, portName } from "../state.js";
import { Card, Empty, ErrorBox, Input, Loading, Mono, PageHeader, Tabs } from "../ui/kit.js";

export function ActivityPage() {
  const { status } = useApp();
  const [tab, setTab] = useState("live");
  const [query, setQuery] = useState("");
  const live = usePolled(() => api.live(), 3000);
  const peers = usePolled(() => api.peers(500), 10000);

  const match = (...fields) => !query || fields.some((f) => String(f ?? "").toLowerCase().includes(query.toLowerCase()));
  const liveRows = (live.data?.live || []).filter((l) => match(l.remote, portName(status, l.inport_id), l.inport_name, l.where));
  const peerRows = (peers.data?.peers || []).filter((p) => match(p.ip, portName(status, p.inport_id), p.inport_name));

  return html`
    <${PageHeader} title="Activity" description="Who is using the mesh: live connections and addresses seen in the last 24 hours." />
    <${Card} flush>
      <div class="toolbar">
        <${Tabs} value=${tab} onChange=${setTab} tabs=${[
          { value: "live", label: "Live connections", count: live.data?.live.length },
          { value: "peers", label: "Seen addresses", count: peers.data?.peers.length },
        ]} />
        <${Input} type="search" class="input search" placeholder="Filter by address or port" value=${query} onInput=${setQuery} />
      </div>
      ${tab === "live" ? html`<${LiveTable} state=${live} rows=${liveRows} status=${status} />` : html`<${PeersTable} state=${peers} rows=${peerRows} status=${status} />`}
    <//>`;
}

function guard(state, rows, empty) {
  if (state.loading && !state.data) return html`<${Loading} />`;
  if (state.error && !state.data) return html`<${ErrorBox} error=${state.error} onRetry=${state.reload} />`;
  if (!rows.length) return empty;
  return null;
}

function LiveTable({ state, rows, status }) {
  const now = Date.now() / 1000;
  return guard(state, rows, html`<${Empty} icon="activity" title="No active connections">Connections appear here while they are open.<//>`)
    ?? html`<div class="table-wrap"><table>
      <thead><tr><th>Remote</th><th>Ingress</th><th>Served by</th><th class="num">Open for</th></tr></thead>
      <tbody>${rows.map((l) => html`<tr>
        <td><${Mono}>${l.remote}<//></td>
        <td><span class="mono">${portName(status, l.inport_id)}</span>${l.inport_name && html` <span class="muted">${l.inport_name}</span>`}</td>
        <td>${l.where}</td>
        <td class="num">${duration(l.since, now)}</td>
      </tr>`)}</tbody>
    </table></div>`;
}

function PeersTable({ state, rows, status }) {
  return guard(state, rows, html`<${Empty} icon="activity" title="No addresses recorded">Visitors of ingress ports are listed here.<//>`)
    ?? html`<div class="table-wrap"><table>
      <thead><tr>
        <th>Address</th><th>Ingress</th><th class="num">Conns</th><th class="num">In</th><th class="num">Out</th>
        <th>First seen</th><th>Last seen</th>
      </tr></thead>
      <tbody>${rows.map((p) => html`<tr>
        <td><${Mono}>${p.ip}<//></td>
        <td><span class="mono">${portName(status, p.inport_id)}</span>${p.inport_name && html` <span class="muted">${p.inport_name}</span>`}</td>
        <td class="num">${p.conns}</td>
        <td class="num">${bytes(p.bytes_in)}</td>
        <td class="num">${bytes(p.bytes_out)}</td>
        <td>${ago(p.first_seen)}</td>
        <td>${ago(p.last_seen)}</td>
      </tr>`)}</tbody>
    </table></div>`;
}
