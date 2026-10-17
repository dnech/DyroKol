// Shared building blocks: buttons, form fields, badges, status, cards,
// menus, tabs and empty states.
import { html, useEffect, useRef, useState } from "../lib/html.js";
import { Icon } from "./icons.js";

export function Button({ variant = "default", size, icon, busy, children, class: cls = "", ...rest }) {
  const classes = ["btn", `btn-${variant}`, size && `btn-${size}`, cls].filter(Boolean).join(" ");
  return html`<button type="button" class=${classes} disabled=${busy || rest.disabled} ...${rest}>
    ${busy ? html`<span class="spinner" />` : icon && html`<${Icon} name=${icon} />`}
    ${children && html`<span>${children}</span>`}
  </button>`;
}

export function IconButton({ icon, label, ...rest }) {
  return html`<button type="button" class="icon-btn" aria-label=${label} title=${label} ...${rest}>
    <${Icon} name=${icon} />
  </button>`;
}

// --- forms ---

export function Field({ label, hint, error, children, wide }) {
  return html`<label class=${`field${wide ? " field-wide" : ""}`}>
    ${label && html`<span class="field-label">${label}</span>`}
    ${children}
    ${error ? html`<span class="field-error">${error}</span>` : hint && html`<span class="field-hint">${hint}</span>`}
  </label>`;
}

export function Input({ value, onInput, ...rest }) {
  return html`<input class="input" value=${value ?? ""} onInput=${(e) => onInput?.(e.currentTarget.value)} ...${rest} />`;
}

export function Textarea({ value, onInput, ...rest }) {
  return html`<textarea class="input textarea" value=${value ?? ""} onInput=${(e) => onInput?.(e.currentTarget.value)} ...${rest} />`;
}

export function Select({ value, onChange, options, ...rest }) {
  return html`<select class="input select" value=${value} onChange=${(e) => onChange?.(e.currentTarget.value)} ...${rest}>
    ${options.map((o) => html`<option value=${o.value} disabled=${o.disabled}>${o.label}</option>`)}
  </select>`;
}

export function Segmented({ value, onChange, options }) {
  return html`<div class="segmented" role="radiogroup">
    ${options.map((o) => html`
      <button type="button" role="radio" aria-checked=${value === o.value}
        class=${value === o.value ? "active" : ""} onClick=${() => onChange(o.value)}>${o.label}</button>`)}
  </div>`;
}

export function Switch({ checked, onChange, label, hint }) {
  return html`<label class="switch-row">
    <span>
      <span class="switch-label">${label}</span>
      ${hint && html`<span class="field-hint">${hint}</span>`}
    </span>
    <span class="switch">
      <input type="checkbox" checked=${checked} onChange=${(e) => onChange(e.currentTarget.checked)} />
      <span class="switch-track"><span class="switch-thumb" /></span>
    </span>
  </label>`;
}

export function FormError({ error }) {
  if (!error) return null;
  return html`<div class="form-error" role="alert"><${Icon} name="alert" /><span>${error}</span></div>`;
}

// --- display ---

export function Badge({ tone = "neutral", children, title }) {
  return html`<span class=${`badge badge-${tone}`} title=${title}>${children}</span>`;
}

// Status pairs a colored dot with a text label: color never carries
// meaning alone.
export function Status({ tone, children, title }) {
  return html`<span class=${`status status-${tone}`} title=${title}><span class="status-dot" />${children}</span>`;
}

export function Mono({ children, title }) {
  return html`<span class="mono" title=${title}>${children}</span>`;
}

export function Card({ title, actions, children, flush, class: cls = "" }) {
  return html`<section class=${`card ${flush ? "card-flush" : ""} ${cls}`}>
    ${(title || actions) && html`<header class="card-head">
      ${title && html`<h2>${title}</h2>`}
      ${actions && html`<div class="card-actions">${actions}</div>`}
    </header>`}
    ${children}
  </section>`;
}

export function PageHeader({ title, description, actions }) {
  return html`<header class="page-head">
    <div>
      <h1>${title}</h1>
      ${description && html`<p class="page-desc">${description}</p>`}
    </div>
    ${actions && html`<div class="page-actions">${actions}</div>`}
  </header>`;
}

export function Empty({ icon = "info", title, children, action }) {
  return html`<div class="empty">
    <div class="empty-icon"><${Icon} name=${icon} size=${22} /></div>
    <div class="empty-title">${title}</div>
    ${children && html`<div class="empty-text">${children}</div>`}
    ${action && html`<div class="empty-action">${action}</div>`}
  </div>`;
}

export function Loading() {
  return html`<div class="loading"><span class="spinner" /> Loading…</div>`;
}

export function ErrorBox({ error, onRetry }) {
  return html`<div class="error-box">
    <${Icon} name="alert" />
    <span>${error?.message || String(error)}</span>
    ${onRetry && html`<${Button} size="sm" onClick=${onRetry}>Retry<//>`}
  </div>`;
}

export function StatTile({ label, value, sub, tone }) {
  return html`<div class="stat">
    <div class="stat-label">${label}</div>
    <div class=${`stat-value ${tone ? `stat-${tone}` : ""}`}>${value}</div>
    ${sub && html`<div class="stat-sub">${sub}</div>`}
  </div>`;
}

export function Tabs({ value, onChange, tabs }) {
  return html`<div class="tabs" role="tablist">
    ${tabs.map((t) => html`
      <button type="button" role="tab" aria-selected=${value === t.value}
        class=${value === t.value ? "active" : ""} onClick=${() => onChange(t.value)}>
        ${t.label}${t.count != null && html`<span class="tab-count">${t.count}</span>`}
      </button>`)}
  </div>`;
}

// Menu is a row-actions dropdown. items: { label, icon, onSelect, danger }.
export function Menu({ items, label = "Actions" }) {
  // The list is positioned in viewport coordinates so table and card
  // containers (overflow: hidden/auto) can never clip it.
  const [pos, setPos] = useState(null);
  const ref = useRef();
  const list = useRef();
  const open = pos != null;
  const close = () => setPos(null);

  const toggle = () => {
    if (open) return close();
    const r = ref.current.getBoundingClientRect();
    const height = items.filter(Boolean).length * 34 + 12;
    const up = r.bottom + height > innerHeight - 8 && r.top > height;
    setPos({ right: innerWidth - r.right, ...(up ? { bottom: innerHeight - r.top + 4 } : { top: r.bottom + 4 }) });
  };

  useEffect(() => {
    if (!open) return undefined;
    const onDoc = (e) => { if (!ref.current?.contains(e.target) && !list.current?.contains(e.target)) close(); };
    const onKey = (e) => { if (e.key === "Escape") close(); };
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    addEventListener("resize", close);
    addEventListener("scroll", close, true);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
      removeEventListener("resize", close);
      removeEventListener("scroll", close, true);
    };
  }, [open]);

  return html`<div class="menu" ref=${ref}>
    <${IconButton} icon="more" label=${label} aria-haspopup="menu" aria-expanded=${open} onClick=${toggle} />
    ${open && html`<div class="menu-list" role="menu" ref=${list} style=${{ ...pos, position: "fixed" }}>
      ${items.filter(Boolean).map((it) => it === "-" ? html`<hr />` : html`
        <button type="button" role="menuitem" class=${it.danger ? "danger" : ""}
          onClick=${() => { close(); it.onSelect(); }}>
          ${it.icon && html`<${Icon} name=${it.icon} />`}${it.label}
        </button>`)}
    </div>`}
  </div>`;
}

export function CopyField({ value, label }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      const ta = document.createElement("textarea");
      ta.value = value;
      document.body.append(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return html`<div class="copy-field">
    ${label && html`<span class="field-label">${label}</span>`}
    <div class="copy-row">
      <code>${value}</code>
      <${Button} size="sm" icon=${copied ? "check" : "copy"} onClick=${copy}>${copied ? "Copied" : "Copy"}<//>
    </div>
  </div>`;
}
