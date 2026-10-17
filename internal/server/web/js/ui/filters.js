// Allow/deny list editor shared by ingress (source) and egress (destination).
import { html } from "../lib/html.js";
import { Field, Textarea } from "./kit.js";

export function FilterEditor({ kind, allow, deny, onAllow, onDeny }) {
  const source = kind === "source";
  const subject = source ? "visitors" : "destinations";
  return html`<div class="filter-editor">
    <div class="field-row">
      <${Field} label="Allow" hint=${`one per line; empty allows all ${subject}`} wide>
        <${Textarea} rows="4" value=${allow} onInput=${onAllow} placeholder=${source ? "10.0.0.0/8\n203.0.113.7" : "10.0.0.0/8\n*.example.com"} spellcheck="false" />
      <//>
      <${Field} label="Deny" hint="takes precedence over allow" wide>
        <${Textarea} rows="4" value=${deny} onInput=${onDeny} placeholder=${source ? "192.0.2.0/24" : "192.0.2.5\n*:25"} spellcheck="false" />
      <//>
    </div>
    <p class="field-hint">Patterns: <code>host</code>, <code>*.domain</code>, <code>ip</code>, <code>10.0.0.0/8</code>${!source && html`, <code>host:port</code>, <code>*:port</code>`}. ${source && "Visitor ports are random, so source filters cannot match a port."}</p>
  </div>`;
}

export function filterSummary(allow, deny) {
  const a = allow?.length || 0;
  const d = deny?.length || 0;
  if (!a && !d) return null;
  return [a && `${a} allow`, d && `${d} deny`].filter(Boolean).join(" · ");
}
