// Minimal observable stores and the hooks that bind them to components.
import { useEffect, useReducer, useRef, useState, useCallback } from "./html.js";

export function createStore(initial) {
  let state = initial;
  const subs = new Set();
  return {
    get: () => state,
    set(patch) {
      const next = typeof patch === "function" ? patch(state) : patch;
      state = { ...state, ...next };
      subs.forEach((fn) => fn());
    },
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
  };
}

// useStore re-renders the component whenever the store changes.
export function useStore(store) {
  const [, force] = useReducer((n) => n + 1, 0);
  useEffect(() => store.subscribe(force), [store]);
  return store.get();
}

// usePolled loads data with fetcher and refreshes it every interval ms while
// the page is visible. reload() refetches immediately.
export function usePolled(fetcher, interval) {
  const [state, setState] = useState({ data: null, error: null, loading: true });
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const reload = useCallback(async () => {
    try {
      const data = await fetcherRef.current();
      setState({ data, error: null, loading: false });
    } catch (error) {
      setState((s) => ({ ...s, error, loading: false }));
    }
  }, []);

  useEffect(() => {
    reload();
    if (!interval) return undefined;
    const tick = () => { if (!document.hidden) reload(); };
    const id = setInterval(tick, interval);
    document.addEventListener("visibilitychange", tick);
    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", tick);
    };
  }, [interval, reload]);

  return { ...state, reload };
}
