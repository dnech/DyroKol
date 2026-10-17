// DOM helpers shared by components.
import { useEffect, useState } from "./html.js";

// useSize tracks the content box of the element held by ref.
export function useSize(ref) {
  const [size, setSize] = useState({ width: 0, height: 0 });
  useEffect(() => {
    const ro = new ResizeObserver(([entry]) => setSize({
      width: Math.floor(entry.contentRect.width),
      height: Math.floor(entry.contentRect.height),
    }));
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, []);
  return size;
}

// useWidth tracks the content width of the element held by ref.
export function useWidth(ref) {
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const ro = new ResizeObserver(([entry]) => setWidth(Math.floor(entry.contentRect.width)));
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, []);
  return width;
}
