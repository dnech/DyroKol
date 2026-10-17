// Display formatting helpers.

const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

export function bytes(n) {
  if (!n) return "0 B";
  let i = 0;
  while (Math.abs(n) >= 1024 && i < UNITS.length - 1) { n /= 1024; i++; }
  return `${i === 0 ? Math.round(n) : n.toFixed(n < 10 ? 1 : 0)} ${UNITS[i]}`;
}

export function rate(bytesPerSec) {
  return `${bytes(bytesPerSec)}/s`;
}

export function compact(n) {
  if (n == null) return "0";
  if (Math.abs(n) < 1000) return String(n);
  const units = ["K", "M", "B"];
  let i = -1;
  while (Math.abs(n) >= 1000 && i < units.length - 1) { n /= 1000; i++; }
  return `${n.toFixed(n < 10 ? 1 : 0)}${units[i]}`;
}

export function ago(ts, now = Date.now() / 1000) {
  if (!ts) return "never";
  const d = Math.max(0, Math.round(now - ts));
  if (d < 5) return "just now";
  if (d < 60) return `${d}s ago`;
  if (d < 3600) return `${Math.floor(d / 60)}m ago`;
  if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
  return `${Math.floor(d / 86400)}d ago`;
}

export function duration(sinceTs, now = Date.now() / 1000) {
  const d = Math.max(0, Math.round(now - sinceTs));
  if (d < 60) return `${d}s`;
  if (d < 3600) return `${Math.floor(d / 60)}m ${d % 60}s`;
  return `${Math.floor(d / 3600)}h ${Math.floor((d % 3600) / 60)}m`;
}

export function dateTime(ts) {
  return new Date(ts * 1000).toLocaleString();
}

export function time(ts) {
  return new Date(ts * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// lines splits a textarea value into trimmed non-empty lines.
export function lines(text) {
  return text.split("\n").map((s) => s.trim()).filter(Boolean);
}
