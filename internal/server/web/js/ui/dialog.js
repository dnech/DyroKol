// Modal dialogs as promises: openDialog(render) shows a dialog and resolves
// with the value passed to close(). confirmDialog() is the common yes/no case.
import { html, useEffect, useRef } from "../lib/html.js";
import { createStore, useStore } from "../lib/store.js";
import { Icon } from "./icons.js";
import { Button } from "./kit.js";

const store = createStore({ stack: [] });
let seq = 0;

// openDialog renders render({ close }) in a modal; resolves to the value
// given to close (undefined when dismissed).
export function openDialog(render, { size = "md" } = {}) {
  return new Promise((resolve) => {
    const id = ++seq;
    const close = (value) => {
      store.set((s) => ({ stack: s.stack.filter((d) => d.id !== id) }));
      resolve(value);
    };
    store.set((s) => ({ stack: [...s.stack, { id, render, close, size }] }));
  });
}

export function confirmDialog({ title, message, confirm = "Confirm", danger = false }) {
  return openDialog(({ close }) => html`
    <${Dialog} title=${title} onClose=${() => close(false)}
      footer=${html`
        <${Button} onClick=${() => close(false)}>Cancel<//>
        <${Button} variant=${danger ? "danger" : "primary"} autofocus onClick=${() => close(true)}>${confirm}<//>`}>
      <p class="dialog-text">${message}</p>
    <//>`, { size: "sm" }).then(Boolean);
}

// Dialog is the frame: title bar, scrollable body, footer actions.
export function Dialog({ title, onClose, footer, children, onSubmit }) {
  const body = html`
    <header class="dialog-head">
      <h2>${title}</h2>
      <button type="button" class="icon-btn" aria-label="Close" onClick=${onClose}><${Icon} name="x" /></button>
    </header>
    <div class="dialog-body">${children}</div>
    ${footer && html`<footer class="dialog-foot">${footer}</footer>`}`;
  if (onSubmit) {
    return html`<form class="dialog-frame" onSubmit=${(e) => { e.preventDefault(); onSubmit(); }}>${body}</form>`;
  }
  return html`<div class="dialog-frame">${body}</div>`;
}

function Layer({ entry, top }) {
  const ref = useRef();
  useEffect(() => {
    if (!top) return undefined;
    const prev = document.activeElement;
    const el = ref.current;
    const focusable = el.querySelector("[autofocus], input, select, textarea, button.primary, button");
    focusable?.focus();
    const onKey = (e) => { if (e.key === "Escape") entry.close(undefined); };
    addEventListener("keydown", onKey);
    return () => { removeEventListener("keydown", onKey); prev?.focus?.(); };
  }, [top]);
  return html`<div class="dialog-backdrop" onMouseDown=${(e) => { if (e.target === e.currentTarget) entry.close(undefined); }}>
    <div ref=${ref} class=${`dialog dialog-${entry.size}`} role="dialog" aria-modal="true">
      ${entry.render({ close: entry.close })}
    </div>
  </div>`;
}

export function DialogHost() {
  const { stack } = useStore(store);
  return stack.map((d, i) => html`<${Layer} key=${d.id} entry=${d} top=${i === stack.length - 1} />`);
}
