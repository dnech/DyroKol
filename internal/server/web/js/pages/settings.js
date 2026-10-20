// Settings: preferences, backup/restore, relay info and lifecycle.
import { html, useState } from "../lib/html.js";
import { api } from "../lib/api.js";
import { mutate, refresh, useAction, useApp } from "../state.js";
import { confirmDialog, Dialog, openDialog } from "../ui/dialog.js";
import { toast } from "../ui/toast.js";
import { Button, Card, Field, FormError, Input, Mono, PageHeader, Switch } from "../ui/kit.js";

export function SettingsPage() {
  const { status, config } = useApp();

  const setAutostart = (v) => mutate(() => api.saveSettings({ autostart: v }), v ? "Autostart enabled" : "Autostart disabled");

  const download = async (secrets) => {
    try {
      const res = await api.backup(secrets);
      const url = URL.createObjectURL(await res.blob());
      const a = Object.assign(document.createElement("a"), { href: url, download: "dyrokol-config.json" });
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      toast.error(e.message);
    }
  };

  const restore = () => openDialog(({ close }) => html`<${RestoreDialog} close=${close} />`);

  const shutdown = async () => {
    const ok = await confirmDialog({
      title: "Shut down the relay?",
      message: "Every tunnel stops. The relay has to be started again on its host; this UI will stop responding.",
      confirm: "Shut down", danger: true,
    });
    if (ok && await mutate(() => api.shutdown(), "Relay is shutting down")) {
      setTimeout(refresh, 1000);
    }
  };

  const info = [
    ["Version", status.version],
    ["Protocol", config?.protocol],
    ["Tunnel listener", config?.listen],
    ["Admin listener", config?.admin_addr || "none (only through an admin machine)"],
    ["Database", config?.db_file],
    ["Config version", status.config_version],
    ["TLS pin", status.fingerprint],
  ];

  return html`
    <${PageHeader} title="Settings" />
    <div class="settings">
      <${Card} title="General">
        <${RelayIdentity} status=${status} />
        <${Switch} checked=${status.autostart} onChange=${setAutostart} label="Start on boot"
          hint="Used by the platform service wrapper (dyrokol service)." />
      <//>

      <${Card} title="Backup and restore">
        <p class="card-text">A backup contains machines (by key hash), ports, routes, the map layout and settings — never machine tokens,
          the database password or traffic history.</p>
        <div class="button-row">
          <${Button} icon="download" onClick=${() => download(true)}>Download backup<//>
          <${Button} icon="download" variant="ghost" onClick=${() => download(false)}>Without proxy passwords<//>
          <${Button} icon="upload" onClick=${restore}>Restore…<//>
        </div>
      <//>

      <${Card} title="About this relay">
        <dl class="info-list">
          ${info.map(([k, v]) => html`<dt>${k}</dt><dd>${v != null ? html`<${Mono}>${String(v)}<//>` : "—"}</dd>`)}
        </dl>
      <//>

      <${Card} title="Danger zone" class="card-danger">
        <div class="danger-row">
          <div>
            <div class="cell-title">Shut down relay</div>
            <div class="sub">Stops the relay process and every tunnel through it.</div>
          </div>
          <${Button} variant="danger" icon="power" onClick=${shutdown}>Shut down<//>
        </div>
      <//>
    </div>`;
}

function RestoreDialog({ close }) {
  const [file, setFile] = useState(null);
  const { busy, error, run } = useAction(async () => {
    if (!file) throw new Error("Choose a backup file");
    const out = await api.restore(await file.text());
    const failed = Object.keys(out.bind_errors || {}).length;
    return failed;
  }, {
    success: (failed) => (failed ? `Restored; ${failed} port(s) could not be bound` : "Configuration restored"),
    onDone: () => close(),
  });
  return html`<${Dialog} title="Restore configuration" onClose=${() => close()} onSubmit=${run}
    footer=${html`<${Button} onClick=${() => close()}>Cancel<//>
      <${Button} type="submit" variant="danger" busy=${busy}>Replace configuration<//>`}>
    <${FormError} error=${error} />
    <p class="dialog-text">Restoring replaces every machine, port and route, and reconnects every agent.
      Traffic history is cleared.</p>
    <label class="file-drop">
      <input type="file" accept="application/json,.json" onChange=${(e) => setFile(e.currentTarget.files[0] || null)} />
      <span>${file ? file.name : "Choose a dyrokol-config.json file"}</span>
    </label>
  <//>`;
}

// RelayIdentity edits the relay's display name (shown in the header) and the
// public address — a domain or an ip — that fills in the client command of the
// "Add machine" dialog.
function RelayIdentity({ status }) {
  const [name, setName] = useState(status.name || "");
  const [addr, setAddr] = useState(status.public_addr || "");
  const dirty = name.trim() !== (status.name || "") || addr.trim() !== (status.public_addr || "");
  const { busy, error, run } = useAction(() => api.saveSettings({ name: name.trim(), public_addr: addr.trim() }),
    { success: "Saved" });
  return html`<form class="relay-identity" onSubmit=${(e) => { e.preventDefault(); run(); }}>
    <${FormError} error=${error} />
    <${Field} label="Relay name" hint="shown in the header of this admin UI">
      <${Input} value=${name} onInput=${setName} maxlength="200" placeholder="e.g. Frankfurt VPS" />
    <//>
    <${Field} label="Public address" hint="domain or ip that agents connect to, port optional (default 7443); used in the client command">
      <${Input} value=${addr} onInput=${setAddr} maxlength="255" spellcheck="false" placeholder="relay.example.com" />
    <//>
    <div class="button-row"><${Button} type="submit" variant="primary" busy=${busy} disabled=${!dirty}>Save<//></div>
  </form>`;
}
