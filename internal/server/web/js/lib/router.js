// Hash router: #/machines → "machines". Works behind any path prefix and
// needs no server-side routes.
import { useEffect, useState } from "./html.js";

function current() {
  return location.hash.replace(/^#\/?/, "").split("?")[0] || "overview";
}

export function useRoute() {
  const [route, setRoute] = useState(current);
  useEffect(() => {
    const onChange = () => setRoute(current());
    addEventListener("hashchange", onChange);
    return () => removeEventListener("hashchange", onChange);
  }, []);
  return route;
}

export function navigate(route) {
  location.hash = `#/${route}`;
}
