// Transient notifications in the corner of the screen.
import { html } from "../lib/html.js";
import { createStore, useStore } from "../lib/store.js";
import { Icon } from "./icons.js";

const store = createStore({ items: [] });
let seq = 0;

function push(kind, text, ttl) {
  const id = ++seq;
  store.set((s) => ({ items: [...s.items, { id, kind, text }] }));
  setTimeout(() => dismiss(id), ttl);
}

function dismiss(id) {
  store.set((s) => ({ items: s.items.filter((t) => t.id !== id) }));
}

export const toast = {
  success: (text) => push("success", text, 3500),
  error: (text) => push("error", text, 7000),
  info: (text) => push("info", text, 3500),
};

const ICONS = { success: "circleCheck", error: "alert", info: "info" };

export function Toasts() {
  const { items } = useStore(store);
  return html`<div class="toasts" role="status" aria-live="polite">
    ${items.map((t) => html`
      <div key=${t.id} class=${`toast toast-${t.kind}`}>
        <${Icon} name=${ICONS[t.kind]} size=${18} />
        <span>${t.text}</span>
        <button class="icon-btn" aria-label="Dismiss" onClick=${() => dismiss(t.id)}><${Icon} name="x" /></button>
      </div>`)}
  </div>`;
}
