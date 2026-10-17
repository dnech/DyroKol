// Route map: a free canvas where every ingress port (left by default) and
// egress port (right by default) is a node, and routes are the links between
// them. The canvas is viewed through a pan/zoom viewport that can fill the
// whole screen. In edit mode nodes can be dragged and the operator can add
// text labels and frames; those annotations lie under the links and the
// nodes. The arrangement is shared and persisted by the server (see
// lib/maplayout.js); the view (pan, zoom) is local to the browser.
//
// Layering, bottom to top: frames, texts, links (SVG), nodes.
import { html, useEffect, useMemo, useRef, useState } from "../lib/html.js";
import { useSize } from "../lib/dom.js";
import { ingressState, useApp } from "../state.js";
import { confirmDialog } from "../ui/dialog.js";
import { Button, IconButton, Segmented, Status } from "../ui/kit.js";
import { rate as fmtRate } from "../lib/format.js";

const NODE_W = 232;
const NODE_H = 54;
const GAP = 10;
const PAD = 24;
const GRID = 10;
const DEFAULT_GAP = 460; // between the default ingress and egress columns
const MIN_VIEW_H = 320;
const MAX_VIEW_H = 640;
const MIN_FRAME = 60;
const MIN_ZOOM = 0.2;
const MAX_ZOOM = 3;
const TEXT_BOX = { w: 200, h: 36 }; // sizes the canvas around a text

export const COLORS = ["neutral", "blue", "green", "orange", "red", "violet"];
const SIZES = [{ value: "S", label: "S" }, { value: "M", label: "M" }, { value: "L", label: "L" }];
const EMPTY = Object.freeze({ nodes: {}, items: [] });

const snap = (v) => Math.round(v / GRID) * GRID;
const clampZoom = (z) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, z));

// buildModel orders the nodes. Ingress ports are sorted by the position of
// their target, which keeps the default arrangement from crossing needlessly.
export function buildModel(status) {
  const relay = status.relay_label;
  const rank = new Map([[relay, 0]]);
  status.clients.forEach((c, i) => rank.set(c.key_hash, i + 1));
  const r = (m) => rank.get(m) ?? 1e6;
  const endpoints = [...status.endpoints].sort((a, b) => r(a.machine) - r(b.machine) || a.id - b.id);
  const position = new Map(endpoints.map((e, i) => [e.id, i]));
  const inports = [...status.inports].sort((a, b) =>
    r(a.machine) - r(b.machine)
    || (position.get(a.endpoint_id) ?? 1e9) - (position.get(b.endpoint_id) ?? 1e9)
    || a.port - b.port);
  return { inports, endpoints, routes: status.routes };
}

// defaultPositions places ports the operator has not positioned yet: ingress
// ports in a left column, egress ports in a right one, below anything that
// is already placed in that column. It does not depend on the screen size,
// so the picture does not shift when the map goes full screen.
function defaultPositions(model, saved) {
  const pos = {};
  for (const [kind, list, x] of [["in", model.inports, PAD], ["eg", model.endpoints, PAD + NODE_W + DEFAULT_GAP]]) {
    let next = PAD;
    for (const n of list) {
      const s = saved[`${kind}:${n.id}`];
      if (s) next = Math.max(next, s.y + NODE_H + GAP);
    }
    for (const n of list) {
      const key = `${kind}:${n.id}`;
      pos[key] = saved[key] ?? { x, y: next };
      if (!saved[key]) next += NODE_H + GAP;
    }
  }
  return pos;
}

// link draws a curve from the ingress node a to the egress node b, leaving
// by the side that faces the other node.
function link(a, b) {
  const ay = a.y + NODE_H / 2;
  const by = b.y + NODE_H / 2;
  const horizontal = (sx, ex) => {
    const mid = (sx + ex) / 2;
    return { x1: sx, y1: ay, x2: ex, y2: by, d: `M${sx},${ay} C${mid},${ay} ${mid},${by} ${ex},${by}` };
  };
  if (b.x >= a.x + NODE_W + 24) return horizontal(a.x + NODE_W, b.x);
  if (b.x + NODE_W + 24 <= a.x) return horizontal(a.x, b.x + NODE_W);
  const down = b.y >= a.y;
  const sx = a.x + NODE_W / 2;
  const ex = b.x + NODE_W / 2;
  const sy = down ? a.y + NODE_H : a.y;
  const ey = down ? b.y : b.y + NODE_H;
  const mid = (sy + ey) / 2;
  return { x1: sx, y1: sy, x2: ex, y2: ey, d: `M${sx},${sy} C${sx},${mid} ${ex},${mid} ${ex},${ey}` };
}

// bounds is the rectangle that holds every node and annotation.
function bounds(pos, items) {
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  const add = (x, y, w, h) => {
    minX = Math.min(minX, x);
    minY = Math.min(minY, y);
    maxX = Math.max(maxX, x + w);
    maxY = Math.max(maxY, y + h);
  };
  for (const p of Object.values(pos)) add(p.x, p.y, NODE_W, NODE_H);
  for (const it of items) add(it.x, it.y, it.kind === "frame" ? it.w : TEXT_BOX.w, it.kind === "frame" ? it.h : TEXT_BOX.h);
  if (minX === Infinity) return { minX: 0, minY: 0, maxX: NODE_W, maxY: NODE_H };
  return { minX, minY, maxX, maxY };
}

// fitView returns the view that shows the whole content centered in a
// viewport of vw x vh. Unless grow is set it never zooms in past 100%.
function fitView(b, vw, vh, grow) {
  const w = b.maxX - b.minX + 2 * PAD;
  const h = b.maxY - b.minY + 2 * PAD;
  let z = Math.min(vw / w, vh / h);
  z = clampZoom(grow ? Math.min(z, 1.5) : Math.min(z, 1));
  return { z, x: (vw - w * z) / 2 - (b.minX - PAD) * z, y: (vh - h * z) / 2 - (b.minY - PAD) * z };
}

const newId = () => `i${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`;

export function RouteMap({ status, layout, onChange, editable = false }) {
  const vp = useRef();
  const size = useSize(vp);
  const [view, setView] = useState({ x: 0, y: 0, z: 1 });
  const viewRef = useRef(view);
  viewRef.current = view;
  const pendingFit = useRef({ mode: "shrink" }); // fit once the layout and the viewport size are known
  const [expanded, setExpanded] = useState(false);
  const [panning, setPanning] = useState(false);
  const [hover, setHover] = useState(null);
  const [tip, setTip] = useState(null); // route under the pointer: { id, x, y } in viewport pixels
  const [selectedId, setSelectedId] = useState(null);
  const [editingId, setEditingId] = useState(null); // annotation whose text is being edited in place
  const [menuId, setMenuId] = useState(null); // annotation whose settings popover (gear) is open
  const [drag, setDrag] = useState(null);
  const live = useRef(null); // the node/annotation drag in progress
  const ptrs = useRef(new Map()); // active pointers on the background (pan, pinch)
  const gesture = useRef(null);
  const model = useMemo(() => buildModel(status), [status]);

  const lay = layout || EMPTY;
  const canEdit = editable && !!layout;
  const base = useMemo(() => defaultPositions(model, lay.nodes), [model, lay.nodes]);
  const pos = drag?.kind === "node" ? { ...base, [drag.key]: { x: drag.x, y: drag.y } } : base;
  const items = drag && drag.kind !== "node"
    ? lay.items.map((it) => (it.id === drag.id ? { ...it, ...drag.patch } : it))
    : lay.items;
  const box = bounds(pos, items);
  // The viewport height is known up front outside full screen; measuring it
  // right after it changed would give the previous value.
  const viewH = Math.round(Math.min(MAX_VIEW_H, Math.max(MIN_VIEW_H, box.maxY - box.minY + 2 * PAD)));
  const vpW = size.width;
  const vpH = expanded ? size.height : viewH - 2;

  // --- view: zoom, pan, fit, full screen ---

  const zoomAt = (factor, cx, cy) => setView((v) => {
    const z = clampZoom(v.z * factor);
    const k = z / v.z;
    return { z, x: cx - (cx - v.x) * k, y: cy - (cy - v.y) * k };
  });
  const zoomBy = (factor) => zoomAt(factor, vpW / 2, vpH / 2);
  const resetZoom = () => setView((v) => {
    const k = 1 / v.z;
    return { z: 1, x: vpW / 2 - (vpW / 2 - v.x) * k, y: vpH / 2 - (vpH / 2 - v.y) * k };
  });
  const fit = () => setView(fitView(box, vpW, vpH, true));

  // Fit once the layout is loaded, and again after the full screen is toggled
  // (the effect runs when the viewport reports its new size).
  const requestFit = () => { pendingFit.current = { mode: "grow", from: `${size.width}x${size.height}` }; };
  useEffect(() => {
    const want = pendingFit.current;
    if (!want || layout === null || !vpW || !vpH) return;
    if (want.from === `${size.width}x${size.height}`) return; // wait until the viewport has resized
    pendingFit.current = null;
    setView(fitView(box, vpW, vpH, want.mode === "grow"));
  }, [size.width, size.height, vpH, layout]);

  // Ctrl/Cmd + wheel (a trackpad pinch) zooms; in full screen the plain wheel
  // does too. Otherwise the wheel scrolls the page as usual.
  useEffect(() => {
    const el = vp.current;
    const onWheel = (e) => {
      if (!(e.ctrlKey || e.metaKey || expanded)) return;
      e.preventDefault();
      const r = el.getBoundingClientRect();
      zoomAt(Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0015)), e.clientX - r.left, e.clientY - r.top);
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [expanded]);

  const toggleExpanded = () => {
    const next = !expanded;
    requestFit();
    setExpanded(next);
    if (next) document.documentElement.requestFullscreen?.().catch(() => {});
    else if (document.fullscreenElement) document.exitFullscreen?.().catch(() => {});
  };
  useEffect(() => {
    if (!expanded) return undefined;
    document.body.style.overflow = "hidden";
    const onFullscreen = () => { if (!document.fullscreenElement) { requestFit(); setExpanded(false); } };
    const onKey = (e) => {
      if (e.key === "Escape" && !document.querySelector(".dialog-backdrop")) { requestFit(); setExpanded(false); }
    };
    document.addEventListener("fullscreenchange", onFullscreen);
    addEventListener("keydown", onKey);
    return () => {
      document.body.style.overflow = "";
      document.removeEventListener("fullscreenchange", onFullscreen);
      removeEventListener("keydown", onKey);
      if (document.fullscreenElement) document.exitFullscreen?.().catch(() => {});
    };
  }, [expanded]);

  // Dragging the background pans; two fingers pan and pinch-zoom.
  const bgDown = (e) => {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    ptrs.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const v = viewRef.current;
    if (ptrs.current.size === 1) {
      gesture.current = { kind: "pan", sx: e.clientX, sy: e.clientY, vx: v.x, vy: v.y, moved: false };
    } else if (ptrs.current.size === 2) {
      const [a, b] = [...ptrs.current.values()];
      const r = vp.current.getBoundingClientRect();
      gesture.current = {
        kind: "pinch", d0: Math.hypot(a.x - b.x, a.y - b.y) || 1,
        mx: (a.x + b.x) / 2 - r.left, my: (a.y + b.y) / 2 - r.top, v0: v,
      };
    }
  };
  const bgMove = (e) => {
    if (!ptrs.current.has(e.pointerId)) return;
    ptrs.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const g = gesture.current;
    if (!g) return;
    if (g.kind === "pan") {
      const dx = e.clientX - g.sx;
      const dy = e.clientY - g.sy;
      if (!g.moved && Math.hypot(dx, dy) < 3) return;
      g.moved = true;
      setPanning(true);
      setView((v) => ({ ...v, x: g.vx + dx, y: g.vy + dy }));
    } else if (ptrs.current.size === 2) {
      const [a, b] = [...ptrs.current.values()];
      const r = vp.current.getBoundingClientRect();
      const z = clampZoom(g.v0.z * (Math.hypot(a.x - b.x, a.y - b.y) / g.d0));
      const mx = (a.x + b.x) / 2 - r.left;
      const my = (a.y + b.y) / 2 - r.top;
      const wx = (g.mx - g.v0.x) / g.v0.z;
      const wy = (g.my - g.v0.y) / g.v0.z;
      setView({ z, x: mx - wx * z, y: my - wy * z });
    }
  };
  const bgUp = (e) => {
    ptrs.current.delete(e.pointerId);
    const g = gesture.current;
    if (ptrs.current.size === 0) {
      gesture.current = null;
      setPanning(false);
      if (g?.kind === "pan" && !g.moved && e.target === e.currentTarget) deselect();
    } else if (ptrs.current.size === 1) {
      const [p] = [...ptrs.current.values()];
      const v = viewRef.current;
      gesture.current = { kind: "pan", sx: p.x, sy: p.y, vx: v.x, vy: v.y, moved: true };
    }
  };

  // --- drag and drop of ports and annotations (pointer events: mouse, pen, touch) ---

  const startDrag = (e, desc) => {
    if (!canEdit || e.button !== 0) return;
    e.stopPropagation();
    e.currentTarget.setPointerCapture(e.pointerId);
    if (desc.select !== undefined) setSelectedId(desc.select);
    live.current = { ...desc, sx: e.clientX, sy: e.clientY, moved: false, current: null };
  };
  const moveDrag = (e) => {
    const d = live.current;
    if (!d) return;
    const z = viewRef.current.z; // screen pixels to map pixels
    const dx = (e.clientX - d.sx) / z;
    const dy = (e.clientY - d.sy) / z;
    if (!d.moved && Math.hypot(e.clientX - d.sx, e.clientY - d.sy) < 3) return;
    d.moved = true;
    if (d.kind === "node") {
      d.current = { kind: "node", key: d.key, x: snap(d.ox + dx), y: snap(d.oy + dy) };
    } else if (d.kind === "move") {
      d.current = { kind: "move", id: d.id, patch: { x: snap(d.ox + dx), y: snap(d.oy + dy) } };
    } else {
      d.current = { kind: "resize", id: d.id, patch: { w: Math.max(MIN_FRAME, snap(d.ow + dx)), h: Math.max(MIN_FRAME, snap(d.oh + dy)) } };
    }
    setDrag(d.current);
  };
  const endDrag = () => {
    const d = live.current;
    live.current = null;
    setDrag(null);
    if (d && !d.moved && d.edit) setEditingId(d.id);
    if (!d?.moved || !d.current) return;
    const cur = d.current;
    if (cur.kind === "node") {
      // freeze every port where it is, so later additions cannot shift the others
      const nodes = {};
      for (const [key, p] of Object.entries(base)) nodes[key] = { x: p.x, y: p.y };
      nodes[cur.key] = { x: cur.x, y: cur.y };
      onChange({ ...lay, nodes });
    } else {
      onChange({ ...lay, items: lay.items.map((it) => (it.id === cur.id ? { ...it, ...cur.patch } : it)) });
    }
  };
  const dragProps = (desc) => ({
    onPointerDown: (e) => startDrag(e, desc),
    onPointerMove: moveDrag,
    onPointerUp: endDrag,
    onPointerCancel: endDrag,
  });

  // --- annotations ---

  const setItems = (next) => onChange({ ...lay, items: next });
  const patchItem = (id, patch) => setItems(lay.items.map((it) => (it.id === id ? { ...it, ...patch } : it)));
  const removeItem = (id) => { setItems(lay.items.filter((it) => it.id !== id)); setSelectedId(null); setMenuId(null); };
  const deselect = () => { setSelectedId(null); setMenuId(null); setEditingId(null); };
  // New annotations appear in the middle of what is on screen.
  const addItem = (kind) => {
    const n = lay.items.length % 8;
    const x = snap((vpW / 2 - view.x) / view.z - 160) + n * GRID;
    const y = snap((vpH / 3 - view.y) / view.z) + n * GRID;
    const id = newId();
    setItems([...lay.items, kind === "frame"
      ? { id, kind, x, y, w: 320, h: 200, text: "Frame", color: "blue" }
      : { id, kind, x, y: y + 20, text: "Text", color: "neutral", size: "M" }]);
    setSelectedId(id);
    setEditingId(id);
  };
  const autoArrange = async () => {
    const ok = await confirmDialog({
      title: "Auto-arrange ports?",
      message: "Every port goes back to its default place. Texts and frames stay where they are.",
      confirm: "Auto-arrange",
    });
    if (ok) onChange({ ...lay, nodes: {} });
  };

  useEffect(() => {
    if (!selectedId || !canEdit) return undefined;
    const onKey = (e) => {
      if (e.target.matches?.("input, textarea, select")) return;
      if (e.key === "Delete" || e.key === "Backspace") { e.preventDefault(); removeItem(selectedId); }
      if (e.key === "Escape") deselect();
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [selectedId, canEdit, lay]);

  // --- links ---

  const inById = new Map(model.inports.map((p) => [p.id, p]));
  const { rates } = useApp();
  const lines = model.routes.map((r) => {
    const a = pos[`in:${r.inport_id}`];
    const b = pos[`eg:${r.endpoint_id}`];
    if (!a || !b) return null;
    return { r, ...link(a, b), state: ingressState(inById.get(r.inport_id)).line || "ok", flow: rates[r.inport_id] };
  }).filter(Boolean);

  const showTip = (id) => (e) => {
    if (drag || panning || e.buttons) return setTip(null);
    const r = vp.current.getBoundingClientRect();
    setTip({ id, x: e.clientX - r.left, y: e.clientY - r.top });
  };
  const hot = hover && !drag && !panning;
  const connected = (r) => hot && (hover.kind === "in" ? r.inport_id === hover.id : r.endpoint_id === hover.id);
  const lit = { in: new Set(), eg: new Set() };
  if (hot) {
    for (const r of model.routes) {
      if (connected(r)) { lit.in.add(r.inport_id); lit.eg.add(r.endpoint_id); }
    }
    lit[hover.kind].add(hover.id);
  }
  const dim = (kind, id) => hot && !lit[kind].has(id);

  // --- rendering ---

  const node = (kind, item) => {
    const key = `${kind}:${item.id}`;
    const p = pos[key];
    const isIn = kind === "in";
    const st = isIn ? ingressState(item) : null;
    const dashed = isIn ? !item.route_id : !item.route_count;
    const cls = ["rm-node", isIn ? "rm-in" : "rm-out", dashed && "rm-dashed", dim(kind, item.id) && "rm-dim", drag?.key === key && "rm-dragging"]
      .filter(Boolean).join(" ");
    return html`<div key=${key} class=${cls} title=${isIn ? `${item.machine_label}:${item.port}` : item.display}
        style=${{ left: `${p.x}px`, top: `${p.y}px`, width: `${NODE_W}px`, height: `${NODE_H}px` }}
        onMouseEnter=${() => setHover({ kind, id: item.id })} onMouseLeave=${() => setHover(null)}
        ...${dragProps({ kind: "node", key, ox: p.x, oy: p.y })}>
      <span class="rm-title">
        ${isIn
          ? html`<span class="mono strong">:${item.port}</span>`
          : html`<span class="mono strong rm-target">${item.type === "socks5" ? "SOCKS5" : `${item.host}:${item.port}`}</span>`}
        ${item.label && html`<span class="rm-label">${item.label}</span>`}
      </span>
      <span class="rm-sub">
        <span class="rm-machine">${item.machine_label}</span>
        ${isIn
          ? html`<${Status} tone=${st.tone}>${st.text}<//>`
          : html`<span>${item.route_count ? `${item.route_count} route${item.route_count > 1 ? "s" : ""}` : "unused"}</span>
              ${!item.online && html`<${Status} tone="offline">offline<//>`}`}
      </span>
    </div>`;
  };

  const stop = (e) => e.stopPropagation();
  const annotation = (it) => {
    const isSelected = canEdit && it.id === selectedId;
    const editing = canEdit && it.id === editingId;
    const cls = ["rm-item", `rm-${it.kind}`, `rm-c-${it.color}`, it.kind === "text" && `rm-s-${it.size}`, isSelected && "rm-selected"]
      .filter(Boolean).join(" ");
    const style = it.kind === "frame"
      ? { left: `${it.x}px`, top: `${it.y}px`, width: `${it.w}px`, height: `${it.h}px` }
      : { left: `${it.x}px`, top: `${it.y}px` };
    // The text is edited right on the canvas: a click turns it into an input,
    // leaving the input turns it back into text.
    const content = editing
      ? html`<input class="rm-edit" value=${it.text} maxlength="200" ref=${(el) => el && document.activeElement !== el && el.focus()}
          aria-label=${it.kind === "frame" ? "Frame label" : "Text"}
          onPointerDown=${stop} onInput=${(e) => patchItem(it.id, { text: e.target.value })}
          onBlur=${() => setEditingId(null)}
          onKeyDown=${(e) => { if (e.key === "Enter" || e.key === "Escape") { e.stopPropagation(); e.target.blur(); } }} />`
      : it.text;
    const label = it.kind === "frame"
      ? html`<span class="rm-frame-label" ...${dragProps({ kind: "move", id: it.id, ox: it.x, oy: it.y, select: it.id, edit: true })}>${content}</span>`
      : content;
    return html`<div key=${it.id} class=${cls} style=${style}
        ...${dragProps({ kind: "move", id: it.id, ox: it.x, oy: it.y, select: it.id, edit: it.kind === "text" })}>
      ${label}
      ${canEdit && !editing && html`<span class="rm-tools" onPointerDown=${stop}>
        ${isSelected && html`<${IconButton} icon="check" label="Done" onClick=${deselect} />
          <${IconButton} icon="trash" label="Delete" onClick=${() => removeItem(it.id)} />`}
        <${IconButton} icon="settings" label="Settings" onClick=${() => setMenuId(menuId === it.id ? null : it.id)} />
      </span>`}
      ${canEdit && menuId === it.id && html`<div class="rm-menu" onPointerDown=${stop}>
        <span class="rm-swatches" role="radiogroup" aria-label="Color">
          ${COLORS.map((c) => html`<button key=${c} type="button" role="radio" aria-checked=${it.color === c}
            class=${`rm-swatch rm-c-${c}${it.color === c ? " on" : ""}`} aria-label=${c} title=${c}
            onClick=${() => patchItem(it.id, { color: c })} />`)}
        </span>
        ${it.kind === "text" && html`<${Segmented} value=${it.size} onChange=${(v) => patchItem(it.id, { size: v })} options=${SIZES} />`}
      </div>`}
      ${isSelected && it.kind === "frame" && html`<span class="rm-resize" title="Resize"
        ...${dragProps({ kind: "resize", id: it.id, ow: it.w, oh: it.h, select: it.id })} />`}
    </div>`;
  };

  const empty = !model.inports.length && !model.endpoints.length && !lay.items.length;
  const worldStyle = { transform: `translate(${view.x}px, ${view.y}px) scale(${view.z})` };
  const gridSize = 20 * view.z;
  const viewportStyle = {
    height: expanded ? undefined : `${viewH}px`,
    backgroundSize: `${gridSize}px ${gridSize}px`,
    backgroundPosition: `${view.x}px ${view.y}px`,
  };

  return html`<div class=${`routemap${canEdit ? " rm-editable" : ""}${expanded ? " rm-expanded" : ""}`}>
    <div class="rm-toolbar">
      ${canEdit && html`
        <${Button} size="sm" icon="plus" onClick=${() => addItem("text")}>Text<//>
        <${Button} size="sm" icon="plus" onClick=${() => addItem("frame")}>Frame<//>
        <${Button} size="sm" variant="ghost" onClick=${autoArrange}>Auto-arrange<//>`}
      <span class="rm-hint">${canEdit
        ? "Drag ports to move them, the background to pan, Ctrl + wheel to zoom. Texts and frames lie under the links. Saved for everyone."
        : "Drag the background to pan, Ctrl + wheel to zoom."}</span>
      <div class="rm-zoom">
        <${IconButton} icon="minus" label="Zoom out" onClick=${() => zoomBy(1 / 1.25)} />
        <button type="button" class="rm-zoom-level" title="Reset to 100%" onClick=${resetZoom}>${Math.round(view.z * 100)}%</button>
        <${IconButton} icon="plus" label="Zoom in" onClick=${() => zoomBy(1.25)} />
        <${IconButton} icon="fit" label="Fit to view" onClick=${fit} />
        <${IconButton} icon=${expanded ? "minimize" : "maximize"} label=${expanded ? "Exit full screen" : "Full screen"} onClick=${toggleExpanded} />
      </div>
    </div>
    <div class=${`rm-viewport${panning ? " rm-panning" : ""}`} ref=${vp} style=${viewportStyle}
        onPointerDown=${bgDown} onPointerMove=${bgMove} onPointerUp=${bgUp} onPointerCancel=${bgUp}>
      ${empty ? html`<div class="rm-empty">Nothing to show yet — add ingress and egress ports.</div>` : size.width > 0 && html`
      <div class="rm-world" style=${worldStyle}>
        ${items.filter((it) => it.kind === "frame").map(annotation)}
        ${items.filter((it) => it.kind === "text").map(annotation)}
        <svg class="rm-lines" width="1" height="1" aria-hidden="true">
          ${lines.map((l) => html`<g key=${l.r.id} class=${`rm-line rm-${l.state}${hot && !connected(l.r) ? " rm-dim" : ""}${connected(l.r) || tip?.id === l.r.id ? " rm-hot" : ""}`}>
            <path d=${l.d} fill="none" />
            <path class="rm-hit" d=${l.d} fill="none" onPointerEnter=${showTip(l.r.id)} onPointerMove=${showTip(l.r.id)} onPointerLeave=${() => setTip(null)} />
            <circle cx=${l.x1} cy=${l.y1} r="3.5" /><circle cx=${l.x2} cy=${l.y2} r="3.5" />
          </g>`)}
          <${FlowLayer} lines=${lines.map((l) => ({ id: l.r.id, d: l.d, flow: l.flow, dim: hot && !connected(l.r), hot: connected(l.r) || tip?.id === l.r.id }))} />
        </svg>
        ${model.inports.map((p) => node("in", p))}
        ${model.endpoints.map((e) => node("eg", e))}
      </div>`}
      ${tip && !empty && html`<${RouteTip} tip=${tip} route=${model.routes.find((r) => r.id === tip.id)}
        inport=${inById.get(model.routes.find((r) => r.id === tip.id)?.inport_id)} rates=${rates} width=${vpW} />`}
    </div>
    <${MapLegend} />
    <ul class="sr-only">
      ${model.routes.map((r) => {
        const p = inById.get(r.inport_id);
        const e = model.endpoints.find((x) => x.id === r.endpoint_id);
        return html`<li>${p?.machine_label}:${p?.port} routes to ${e?.display}</li>`;
      })}
    </ul>
  </div>`;
}

export function MapLegend() {
  return html`<div class="rm-legend" aria-hidden="true">
    <span><i class="rm-key rm-key-ok" />forwarding</span>
    <span><i class="rm-key rm-key-warn" />target offline</span>
    <span><i class="rm-key rm-key-crit" />bind failed</span>
    <span><i class="rm-key rm-key-off" />route switched off</span>
    <span><i class="rm-dotkey rm-dotkey-in" />traffic to the target</span>
    <span><i class="rm-dotkey rm-dotkey-out" />traffic back</span>
    <span><i class="rm-key rm-key-dash" />not routed / unused</span>
  </div>`;
}

// RouteTip shows the current numbers of the route under the pointer.
function RouteTip({ tip, route, inport, rates, width }) {
  if (!route || !inport) return null;
  const flow = rates[route.inport_id] || {};
  const left = tip.x + 190 > width ? tip.x - 190 : tip.x + 14;
  return html`<div class="tooltip rm-tip" style=${`left:${Math.max(4, left)}px;top:${tip.y + 14}px`}>
    <div class="tooltip-title">${route.inport_label || `:${route.inport}`} → ${route.endpoint_label || route.endpoint}</div>
    ${!route.enabled && html`<div class="tooltip-row"><strong>route is switched off</strong></div>`}
    <div class="tooltip-row">Connections<strong>${inport.active || 0}</strong></div>
    <div class="tooltip-row"><i class="rm-dotkey rm-dotkey-in" />Incoming<strong>${fmtRate(flow.in?.rate || 0)}</strong></div>
    <div class="tooltip-row"><i class="rm-dotkey rm-dotkey-out" />Outgoing<strong>${fmtRate(flow.out?.rate || 0)}</strong></div>
  </div>`;
}

// FlowLayer animates the dots travelling along the routes: blue from the
// ingress to the egress port, orange back. The dots live outside of the render
// cycle (one requestAnimationFrame loop moves them), so a new status snapshot
// never resets them: it only changes the target — how many dots a route should
// have, how fast they go and how visible they are. Surplus dots keep moving
// and fade out; new dots fade in at the route's start; speed changes ease in.
const SVG_NS = "http://www.w3.org/2000/svg";
// The dot is 1.8 times as wide as the line (sticks out 0.4 of the line width
// on each side), the diameter rounded up to a whole pixel: 2 → 4, 3 → 6. Keep LINE_W and LINE_W_HOT in step with the CSS.
const DOT_RATIO = 1.8;
const LINE_W = 2;
const LINE_W_HOT = 3;
const dotRadius = (hot) => Math.ceil((hot ? LINE_W_HOT : LINE_W) * DOT_RATIO) / 2; // diameter rounded up to whole pixels
const reduceMotion = () => matchMedia("(prefers-reduced-motion: reduce)").matches;

// dotTargets maps a rate (bytes/s, log scale: it spans many orders of
// magnitude) to the number of dots and the seconds one dot needs for a route.
function dotTargets(rate) {
  const log = Math.log10(rate + 1);
  return {
    count: log < 3 ? 1 : log < 5 ? 2 : log < 7 ? 3 : 4,
    secs: Math.min(8, Math.max(0.9, 8.5 - 1.2 * log)),
  };
}

// gapMiddle returns where on a route (0..1, circular) a new dot goes so that
// it sits in the middle of the largest gap between the dots already there.
function gapMiddle(ps) {
  if (!ps.length) return 0;
  const sorted = [...ps].sort((a, b) => a - b);
  let best = 0;
  let at = 0;
  sorted.forEach((p, i) => {
    const next = i + 1 < sorted.length ? sorted[i + 1] : sorted[0] + 1;
    if (next - p > best) { best = next - p; at = p + best / 2; }
  });
  return at % 1;
}

function FlowLayer({ lines }) {
  const layer = useRef(null);
  const latest = useRef(lines);
  latest.current = lines;

  useEffect(() => {
    const g = layer.current;
    if (!g || reduceMotion()) return undefined;
    const tracks = new Map(); // "route:dir" → { dots, speed, geo }
    const geos = new Map();   // path data → { path, len } (measuring only)
    const geoOf = (d) => {
      let geo = geos.get(d);
      if (!geo) {
        const path = document.createElementNS(SVG_NS, "path");
        path.setAttribute("d", d);
        geo = { path, len: path.getTotalLength() };
        geos.set(d, geo);
        if (geos.size > 200) geos.delete(geos.keys().next().value);
      }
      return geo;
    };
    let last = performance.now();
    let raf = 0;

    const frame = (now) => {
      raf = requestAnimationFrame(frame);
      const dt = Math.min(0.1, (now - last) / 1000);
      last = now;
      const seen = new Set();
      for (const line of latest.current) {
        for (const dir of ["in", "out"]) {
          const key = `${line.id}:${dir}`;
          const flow = line.flow?.[dir];
          let tr = tracks.get(key);
          if (!tr && !flow) continue;
          if (!tr) { tr = { dots: [], speed: 0 }; tracks.set(key, tr); }
          seen.add(key);
          tr.geo = geoOf(line.d);
          let want = 0;
          let alpha = 0;
          if (flow) {
            const t = dotTargets(flow.rate);
            want = t.count;
            alpha = flow.level * (line.dim ? 0.18 : 1);
            const goal = 1 / t.secs;
            tr.speed += (goal - tr.speed) * Math.min(1, dt * 1.5);
            if (!tr.speed) tr.speed = goal;
          }
          // retire the dots beyond what the route needs, newest first
          let live = tr.dots.filter((d) => d.want).length;
          for (let i = tr.dots.length - 1; i >= 0 && live > want; i--) {
            if (tr.dots[i].want) { tr.dots[i].want = false; live--; }
          }
          while (live < want) {
            const el = document.createElementNS(SVG_NS, "circle");
            el.setAttribute("class", `rm-dot rm-dot-${dir}`);
            g.appendChild(el);
            tr.dots.push({ el, p: gapMiddle(tr.dots.filter((d) => d.want).map((d) => d.p)), a: 0, want: true });
            live++;
          }
          tr.r = dotRadius(line.hot);
          for (const dot of tr.dots) {
            dot.p = (dot.p + tr.speed * dt) % 1;
            const target = dot.want ? alpha : 0;
            dot.a += (target - dot.a) * Math.min(1, dt * 2.5);
          }
        }
      }
      for (const [key, tr] of tracks) {
        tr.dots = tr.dots.filter((dot) => {
          if (seen.has(key) && (dot.want || dot.a > 0.02)) return true;
          dot.el.remove();
          return false;
        });
        if (!tr.dots.length) { tracks.delete(key); continue; }
        const { path, len } = tr.geo;
        const reverse = key.endsWith(":out");
        for (const dot of tr.dots) {
          const pt = path.getPointAtLength((reverse ? 1 - dot.p : dot.p) * len);
          dot.el.setAttribute("r", tr.r);
          dot.el.setAttribute("cx", pt.x.toFixed(1));
          dot.el.setAttribute("cy", pt.y.toFixed(1));
          dot.el.style.opacity = dot.a.toFixed(2);
        }
      }
    };
    raf = requestAnimationFrame(frame);
    return () => { cancelAnimationFrame(raf); g.replaceChildren(); };
  }, []);

  return html`<g class="rm-dots" ref=${layer} />`;
}
