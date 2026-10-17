// Ingress ports: where traffic enters the mesh. Where each port sends its
// traffic is managed on the Routes page; the table only shows it.
import { html, useState } from "../lib/html.js";
import { api } from "../lib/api.js";
import { bytes, lines } from "../lib/format.js";
import { navigate } from "../lib/router.js";
import { machineOptions, mutate, useAction, useApp } from "../state.js";
import { confirmDialog, Dialog, openDialog } from "../ui/dialog.js";
import { FilterEditor, filterSummary } from "../ui/filters.js";
import {
  Badge, Button, Card, Empty, Field, FormError, Input, Menu, PageHeader, Select, Status,
} from "../ui/kit.js";

export function IngressPage() {
  const { status } = useApp();
  const add = () => openDialog(({ close }) => html`<${InPortDialog} close=${close} />`, { size: "lg" });

  return html`
    <${PageHeader} title="Ingress ports"
      description="Ports where traffic enters the mesh, bound on the relay or on a machine. Connect them to egress ports on the Routes page."
      actions=${html`<${Button} variant="primary" icon="plus" onClick=${add}>Add ingress port<//>`} />
    <${Card} flush>
      ${status.inports.length ? html`<div class="table-wrap"><table>
        <thead><tr>
          <th>Port</th><th>Bound on</th><th>Sends traffic to</th><th>Status</th>
          <th class="num">Active</th><th class="num">Conns</th><th class="num">In</th><th class="num">Out</th>
          <th class="actions-col"></th>
        </tr></thead>
        <tbody>${status.inports.map((p) => html`<${InPortRow} key=${p.id} p=${p} status=${status} />`)}</tbody>
      </table></div>` : html`<${Empty} icon="ingress" title="No ingress ports"
          action=${html`<${Button} variant="primary" icon="plus" onClick=${add}>Add ingress port<//>`}>
          Add a port, then connect it to an egress port on the Routes page to start forwarding.
        <//>`}
    <//>`;
}

function portStatus(p) {
  if (p.bind_error) return html`<${Status} tone="critical" title=${p.bind_error}>bind failed<//>`;
  if (!p.route_id) return html`<${Status} tone="warning">not routed<//>`;
  if (!p.route_enabled) return html`<${Status} tone="offline">route off<//>`;
  if (!p.endpoint_online) return html`<${Status} tone="serious">target offline<//>`;
  return html`<${Status} tone="good">forwarding<//>`;
}

function InPortRow({ p, status }) {
  const name = `:${p.port}`;
  const filters = filterSummary(p.allow, p.deny);
  const edit = () => openDialog(({ close }) => html`<${InPortDialog} close=${close} port=${p} />`, { size: "lg" });
  const remove = async () => {
    const ok = await confirmDialog({
      title: `Delete ingress ${name}?`,
      message: "The port is released and its route removed; live connections are dropped.",
      confirm: "Delete", danger: true,
    });
    if (ok) mutate(() => api.deleteInPort(p.id), `Ingress ${name} deleted`);
  };

  return html`<tr class=${p.bind_error ? "row-critical" : ""}>
    <td>
      <div class="cell-title mono">${name}</div>
      <div class="sub">${p.label || html`<span class="muted">no label</span>`}
        ${filters && html` · <${Badge} title="source filter">${filters}<//>`}</div>
    </td>
    <td>${p.machine === status.relay_label ? "relay" : p.machine_label}</td>
    <td>
      ${p.target
        ? html`<div class="mono">${p.target}</div>
            <div class="sub"><a href="#/routes">change on Routes</a></div>`
        : html`<span class="muted">not routed</span>
            <div class="sub"><a href="#/routes">connect on Routes</a></div>`}
    </td>
    <td>${portStatus(p)}${p.bind_error && html`<div class="sub error-text">${p.bind_error}</div>`}</td>
    <td class="num">${p.active}</td>
    <td class="num">${p.conns_total}</td>
    <td class="num">${bytes(p.bytes_in)}</td>
    <td class="num">${bytes(p.bytes_out)}</td>
    <td class="actions-col"><${Menu} items=${[
      { label: "Edit", icon: "edit", onSelect: edit },
      { label: "Routes…", icon: "route", onSelect: () => navigate("routes") },
      "-",
      { label: "Delete", icon: "trash", danger: true, onSelect: remove },
    ]} /></td>
  </tr>`;
}

// InPortDialog creates a port or edits label and filters of an existing one.
function InPortDialog({ close, port }) {
  const { status } = useApp();
  const editing = !!port;
  const [form, setForm] = useState({
    machine: port?.machine ?? status.relay_label,
    port: port ? String(port.port) : "",
    label: port?.label ?? "",
    allow: (port?.allow || []).join("\n"),
    deny: (port?.deny || []).join("\n"),
  });
  const set = (k) => (v) => setForm((f) => ({ ...f, [k]: v }));

  const { busy, error, run } = useAction(async () => {
    const filters = { allow: lines(form.allow), deny: lines(form.deny) };
    if (editing) return api.updateInPort(port.id, { port: Number(form.port), label: form.label.trim(), ...filters });
    return api.createInPort({
      machine: form.machine, port: Number(form.port), label: form.label.trim(), ...filters,
    });
  }, { success: editing ? "Ingress port saved" : "Ingress port added", onDone: () => close() });

  return html`<${Dialog} title=${editing ? `Edit ingress ${port.machine_label}:${port.port}` : "Add ingress port"}
    onClose=${() => close()} onSubmit=${run}
    footer=${html`<${Button} onClick=${() => close()}>Cancel<//>
      <${Button} type="submit" variant="primary" busy=${busy}>${editing ? "Save" : "Add port"}<//>`}>
    <${FormError} error=${error} />
    <div class="field-row">
      <${Field} label="Bound on" hint=${editing ? "the machine cannot be changed; delete and re-create the port to move it" : "machine that listens on the port"} wide>
        <${Select} value=${form.machine} onChange=${set("machine")} options=${machineOptions(status)} disabled=${editing} />
      <//>
      <${Field} label="Port">
        <${Input} type="number" min="1" max="65535" required value=${form.port} onInput=${set("port")} autofocus />
      <//>
    </div>
    <${Field} label="Label" hint="optional, e.g. web or ssh">
      <${Input} value=${form.label} onInput=${set("label")} maxlength="200" />
    <//>
    <h3 class="section-title">Source filter</h3>
    <${FilterEditor} kind="source" allow=${form.allow} deny=${form.deny} onAllow=${set("allow")} onDeny=${set("deny")} />
  <//>`;
}
