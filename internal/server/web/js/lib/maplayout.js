// The shared layout of the route map: where each port sits and the
// annotations (texts, frames) drawn under the ports and links. It is loaded
// once and saved, debounced, after every change.
import { useEffect, useRef, useState } from "./html.js";
import { api } from "./api.js";
import { toast } from "../ui/toast.js";

const SAVE_DELAY = 500;

export const EMPTY_LAYOUT = Object.freeze({ nodes: {}, items: [] });

// useMapLayout returns { layout, update }; layout is null until loaded.
export function useMapLayout() {
  const [layout, setLayout] = useState(null);
  const pending = useRef(null);
  const timer = useRef(0);

  const flush = () => {
    clearTimeout(timer.current);
    const next = pending.current;
    if (!next) return;
    pending.current = null;
    api.saveMap(next).catch((e) => toast.error(`Map layout not saved: ${e.message}`));
  };

  useEffect(() => {
    let alive = true;
    api.getMap().then((l) => alive && setLayout(l)).catch(() => alive && setLayout(EMPTY_LAYOUT));
    return () => { alive = false; flush(); };
  }, []);

  const update = (next) => {
    setLayout(next);
    pending.current = next;
    clearTimeout(timer.current);
    timer.current = setTimeout(flush, SAVE_DELAY);
  };
  return { layout, update };
}
