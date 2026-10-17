// Egress ports: where tunnels exit — a service dialed directly from a
// machine, or a SOCKS5 exit built into the machine (no host or port: the
// SOCKS5 client picks the destination and the machine connects to it).
import { html, useState } from "../lib/html.js";
import { api } from "../lib/api.js";
import { lines } from "../lib/format.js";
import { machineOptions, mutate, useAction, useApp } from "../state.js";
import { confirmDialog, Dialog, openDialog } from "../ui/dialog.js";
import { FilterEditor, filterSummary } from "../ui/filters.js";
import {
  Badge, Button, Card, Empty, Field, FormError, Input, Menu, PageHeader, Segmented, Select, Status,
} from "../ui/kit.js";

const TYPES = [
  { value: "direct", label: "Direct" },
  { value: "socks5", label: "SOCKS5 exit" },
];

export function EgressPage() {
  const { status } = useApp();
  const add = () => openDialog(({ close }) => html`<${EndpointDialog} close=${close} />`, { size: "lg" });

  return html`
    <${PageHeader} title="Egress ports"
      description="Where a tunnel exits: the machine that opens the final connection, to a fixed target or to whatever a SOCKS5 client asks for."
      actions=${html`<${Button} variant="primary" icon="plus" onClick=${add}>Add egress port<//>`} />
    <${Card} flush>
      ${status.endpoints.length ? html`<div class="table-wrap"><table>
        <thead><tr>
          <th>Target</th><th>Exits on</th><th>Type</th><th>Status</th><th class="num">Routes</th><th class="actions-col"></th>
        </tr></thead>
        <tbody>${status.endpoints.map((e) => html`<${EndpointRow} key=${e.id} e=${e} />`)}</tbody>
      </table></div>` : html`<${Empty} icon="egress" title="No egress ports"
          action=${html`<${Button} variant="primary" icon="plus" onClick=${add}>Add egress port<//>`}>
          Add the service you want to expose, then route an ingress port to it.
        <//>`}
    <//>`;
}

function EndpointRow({ e }) {
  const target = e.type === "socks5" ? "SOCKS5 exit" : `${e.host}:${e.port}`;
  const filters = filterSummary(e.allow, e.deny);
  const edit = () => openDialog(({ close }) => html`<${EndpointDialog} close=${close} endpoint=${e} />`, { size: "lg" });
  const remove = async () => {
    const ok = await confirmDialog({
      title: `Delete egress ${target}?`,
      message: e.route_count
        ? `${e.route_count} ingress port(s) route here; they become unrouted and their live connections are dropped.`
        : "Nothing routes here yet.",
      confirm: "Delete", danger: true,
    });
    if (ok) mutate(() => api.deleteEndpoint(e.id), `Egress ${target} deleted`);
  };
  return html`<tr>
    <td>
      <div class="cell-title mono">${target}</div>
      <div class="sub">${e.label || html`<span class="muted">no label</span>`}
        ${filters && html` · <${Badge} title="destination filter">${filters}<//>`}</div>
    </td>
    <td>${e.machine_label}</td>
    <td>
      <${Badge} tone=${e.type === "socks5" ? "accent" : "neutral"}>${e.type}<//>
      ${e.type === "socks5" && html`<div class="sub">${e.socks5_user ? "login required" : "no login"}</div>`}
    </td>
    <td>${e.online ? html`<${Status} tone="good">online<//>` : html`<${Status} tone="offline">machine offline<//>`}</td>
    <td class="num">${e.route_count}</td>
    <td class="actions-col"><${Menu} items=${[
      { label: "Edit", icon: "edit", onSelect: edit },
      "-",
      { label: "Delete", icon: "trash", danger: true, onSelect: remove },
    ]} /></td>
  </tr>`;
}

// EndpointDialog creates an endpoint or edits one: label, filter, target and
// proxy can change; the machine and the type are fixed.
function EndpointDialog({ close, endpoint }) {
  const { status } = useApp();
  const editing = !!endpoint;
  const [form, setForm] = useState({
    machine: endpoint?.machine ?? status.relay_label,
    type: endpoint?.type ?? "direct",
    host: endpoint?.host ?? "",
    port: endpoint?.port ? String(endpoint.port) : "",
    socks5_user: endpoint?.socks5_user ?? "",
    socks5_pass: endpoint?.socks5_pass ?? "",
    label: endpoint?.label ?? "",
    allow: (endpoint?.allow || []).join("\n"),
    deny: (endpoint?.deny || []).join("\n"),
  });
  const set = (k) => (v) => setForm((f) => ({ ...f, [k]: v }));

  const { busy, error, run } = useAction(() => {
    const common = { label: form.label.trim(), allow: lines(form.allow), deny: lines(form.deny) };
    const body = form.type === "socks5"
      ? { ...common, socks5_user: form.socks5_user, socks5_pass: form.socks5_pass }
      : { ...common, host: form.host.trim(), port: Number(form.port) };
    if (editing) return api.updateEndpoint(endpoint.id, body);
    return api.createEndpoint({ ...body, machine: form.machine, type: form.type });
  }, { success: editing ? "Egress port saved" : "Egress port added", onDone: () => close() });

  return html`<${Dialog} title=${editing ? `Edit egress ${endpoint.type === "socks5" ? "SOCKS5 exit" : `${endpoint.host}:${endpoint.port}`}` : "Add egress port"}
    onClose=${() => close()} onSubmit=${run}
    footer=${html`<${Button} onClick=${() => close()}>Cancel<//>
      <${Button} type="submit" variant="primary" busy=${busy}>${editing ? "Save" : "Add port"}<//>`}>
    <${FormError} error=${error} />
    <${Field} label="Exits on" hint=${editing ? "machine and type cannot be changed; delete and re-create the port" : "machine that opens the final connection"}>
      <${Select} value=${form.machine} onChange=${set("machine")} options=${machineOptions(status)} disabled=${editing} />
    <//>
    <${Field} label="Type">
      ${editing
        ? html`<${Select} value=${form.type} options=${TYPES} disabled />`
        : html`<${Segmented} value=${form.type} onChange=${set("type")} options=${TYPES} />`}
    <//>
    ${form.type === "socks5"
      ? html`<fieldset class="fieldset">
        <legend>Built-in SOCKS5 server</legend>
        <p class="muted">Runs on the exit machine only. Route an ingress port here and point any SOCKS5 client at that port: the machine opens the outgoing connections itself.</p>
        <div class="field-row">
          <${Field} label="User" hint="optional" wide>
            <${Input} value=${form.socks5_user} onInput=${set("socks5_user")} autocomplete="off" />
          <//>
          <${Field} label="Password" hint="optional" wide>
            <${Input} type="password" value=${form.socks5_pass} onInput=${set("socks5_pass")} autocomplete="new-password" />
          <//>
        </div>
      </fieldset>`
      : html`<div class="field-row">
        <${Field} label="Target host" hint="as seen from that machine; default 127.0.0.1" wide>
          <${Input} value=${form.host} onInput=${set("host")} placeholder="127.0.0.1" maxlength="255" spellcheck="false" />
        <//>
        <${Field} label="Port">
          <${Input} type="number" min="1" max="65535" required value=${form.port} onInput=${set("port")} autofocus />
        <//>
      </div>`}
    <${Field} label="Label" hint="optional">
      <${Input} value=${form.label} onInput=${set("label")} maxlength="200" />
    <//>
    <h3 class="section-title">Destination filter</h3>
    <${FilterEditor} kind="destination" allow=${form.allow} deny=${form.deny} onAllow=${set("allow")} onDeny=${set("deny")} />
  <//>`;
}
