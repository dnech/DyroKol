// Machines: agents allowed to connect, their role, presence and tokens.
import { html, useState } from "../lib/html.js";
import { api } from "../lib/api.js";
import { ago } from "../lib/format.js";
import { mutate, useAction, useApp, app } from "../state.js";
import { confirmDialog, Dialog, openDialog } from "../ui/dialog.js";
import {
  Badge, Button, Card, CopyField, Empty, Field, FormError, Input, Menu, Mono, PageHeader, Segmented, Status,
} from "../ui/kit.js";

const ROLES = [
  { value: "client", label: "Client" },
  { value: "admin", label: "Admin" },
];

export function MachinesPage() {
  const { status } = useApp();
  const clients = status.clients;
  const add = () => openDialog(({ close }) => html`<${CreateMachineDialog} close=${close} />`);

  return html`
    <${PageHeader} title="Machines"
      description="Agents that may connect to this relay. Each one authenticates with its own token."
      actions=${html`<${Button} variant="primary" icon="plus" onClick=${add}>Add machine<//>`} />
    <${Card} flush>
      ${clients.length ? html`<div class="table-wrap"><table>
        <thead><tr>
          <th>Machine</th><th>Role</th><th>Status</th><th>Address</th><th>Version</th>
          <th class="num">Ingress</th><th class="num">Egress</th><th class="actions-col"></th>
        </tr></thead>
        <tbody>${clients.map((c) => html`<${MachineRow} key=${c.key_hash} c=${c} now=${status.now} />`)}</tbody>
      </table></div>` : html`<${Empty} icon="machines" title="No machines">Add a machine and start the agent with its token.<//>`}
    <//>`;
}

function MachineRow({ c, now }) {
  const name = c.label || c.prefix;
  const rename = () => openDialog(({ close }) => html`<${RenameDialog} close=${close} c=${c} />`);
  const toggleRole = async () => {
    const next = c.role === "admin" ? "client" : "admin";
    const ok = await confirmDialog({
      title: next === "admin" ? `Promote ${name}?` : `Demote ${name}?`,
      message: next === "admin"
        ? "Admin machines receive the admin UI token and expose the admin UI locally. The machine reconnects."
        : "The machine loses access to the admin UI and reconnects.",
      confirm: next === "admin" ? "Promote" : "Demote",
    });
    if (ok) mutate(() => api.updateClient(c.key_hash, { role: next }), `${name} is now ${next}`);
  };
  const regenerate = async () => {
    const ok = await confirmDialog({
      title: `Regenerate token for ${name}?`,
      message: "The current token stops working immediately and the machine is disconnected until its config is updated.",
      confirm: "Regenerate", danger: true,
    });
    if (!ok) return;
    const out = await mutate(() => api.regenerateClient(c.key_hash));
    if (out) openDialog(({ close }) => html`<${TokenDialog} close=${close} title="New token" token=${out.token} name=${name} role=${c.role} />`);
  };
  const remove = async () => {
    const ok = await confirmDialog({
      title: `Delete ${name}?`,
      message: `This also deletes its ${c.inport_count} ingress port(s), ${c.endpoint_count} egress port(s) and their routes.`,
      confirm: "Delete machine", danger: true,
    });
    if (ok) mutate(() => api.deleteClient(c.key_hash), `${name} deleted`);
  };

  return html`<tr>
    <td>
      <div class="cell-title">${name}</div>
      <${Mono} title=${c.key_hash}>${c.prefix}…<//>
    </td>
    <td><${Badge} tone=${c.role === "admin" ? "accent" : "neutral"}>${c.role}<//></td>
    <td>
      ${c.online ? html`<${Status} tone="good">online<//>` : html`<${Status} tone="offline">offline<//>`}
      <div class="sub">${c.online ? "connected" : `seen ${ago(c.last_seen, now)}`}</div>
    </td>
    <td>${c.last_ip ? html`<${Mono}>${c.last_ip}<//>` : html`<span class="muted">—</span>`}</td>
    <td>${c.agent_version ? html`<${Mono}>${c.agent_version}<//>` : html`<span class="muted">—</span>`}</td>
    <td class="num">${c.inport_count}</td>
    <td class="num">${c.endpoint_count}</td>
    <td class="actions-col"><${Menu} items=${[
      { label: "Rename", icon: "edit", onSelect: rename },
      { label: c.role === "admin" ? "Demote to client" : "Promote to admin", icon: "shield", onSelect: toggleRole },
      { label: "Regenerate token", icon: "key", onSelect: regenerate },
      "-",
      { label: "Delete", icon: "trash", danger: true, onSelect: remove },
    ]} /></td>
  </tr>`;
}

function CreateMachineDialog({ close }) {
  const [label, setLabel] = useState("");
  const [role, setRole] = useState("client");
  const { busy, error, run } = useAction(() => api.createClient({ label: label.trim(), role }), {
    onDone: (out) => {
      close();
      openDialog(({ close: done }) => html`<${TokenDialog} close=${done} title="Machine added"
        token=${out.token} name=${out.client.label || "the machine"} role=${out.client.role} />`);
    },
  });
  return html`<${Dialog} title="Add machine" onClose=${() => close()} onSubmit=${run}
    footer=${html`<${Button} onClick=${() => close()}>Cancel<//>
      <${Button} type="submit" variant="primary" busy=${busy}>Create<//>`}>
    <${FormError} error=${error} />
    <${Field} label="Label" hint="shown in lists, e.g. home-nas">
      <${Input} value=${label} onInput=${setLabel} maxlength="200" autofocus />
    <//>
    <${Field} label="Role" hint=${role === "admin" ? "can open the admin UI through its tunnel" : "runs ingress and egress ports"}>
      <${Segmented} value=${role} onChange=${setRole} options=${ROLES} />
    <//>
  <//>`;
}

// TokenDialog shows a machine's token and a ready agent command. The database
// password goes only into the command of an admin machine: it is needed just to
// unlock the relay after a restart, so ordinary machines do not have to hold it.
function TokenDialog({ close, title, token, name, role }) {
  const admin = role === "admin";
  const listen = app.get().config?.listen || ":7443";
  const port = listen.slice(listen.lastIndexOf(":") + 1);
  const publicAddr = app.get().status?.public_addr || "";
  // an address without a port means the default 7443; add the real port when the relay listens elsewhere
  const host = !publicAddr ? `RELAY_HOST:${port}`
    : /:\d+$/.test(publicAddr) || port === "7443" ? publicAddr : `${publicAddr}:${port}`;
  const cmd = `dyrokol client --server ${host} --token ${token}${admin ? " --db-password DB_PASSWORD" : ""}`;
  const where = publicAddr ? "" : "Replace RELAY_HOST with this relay's public address (a domain or an ip; set it once in Settings). ";
  return html`<${Dialog} title=${title} onClose=${() => close()}
    footer=${html`<${Button} variant="primary" onClick=${() => close()}>Done<//>`}>
    <p class="dialog-text">Tunnel token for <strong>${name}</strong>. It is shown only once — store it now.</p>
    <${CopyField} label="Token" value=${token} />
    <${CopyField} label="Start the agent" value=${cmd} />
    <p class="field-hint">${where}${admin
      ? "Replace DB_PASSWORD with the database password: it is needed only to unlock the relay after a restart."
      : "No database password is needed: it only unlocks the relay after a restart. Add --db-password PASSWORD if this machine should be able to do that."}</p>
  <//>`;
}

function RenameDialog({ close, c }) {
  const [label, setLabel] = useState(c.label);
  const { busy, error, run } = useAction(() => api.updateClient(c.key_hash, { label: label.trim() }),
    { success: "Machine renamed", onDone: () => close() });
  return html`<${Dialog} title="Rename machine" onClose=${() => close()} onSubmit=${run}
    footer=${html`<${Button} onClick=${() => close()}>Cancel<//><${Button} type="submit" variant="primary" busy=${busy}>Save<//>`}>
    <${FormError} error=${error} />
    <${Field} label="Label"><${Input} value=${label} onInput=${setLabel} maxlength="200" autofocus /><//>
  <//>`;
}
