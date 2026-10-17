// Logs: relay events, filterable by level and text.
import { html, useState } from "../lib/html.js";
import { api } from "../lib/api.js";
import { usePolled } from "../lib/store.js";
import { dateTime } from "../lib/format.js";
import { Card, Empty, ErrorBox, Input, Loading, PageHeader, Status, Tabs } from "../ui/kit.js";

const TONE = { info: "info", warn: "warning", error: "critical" };

export function LogsPage() {
  const [level, setLevel] = useState("all");
  const [query, setQuery] = useState("");
  const logs = usePolled(() => api.logs(1000), 10000);
  const all = logs.data?.logs || [];
  const count = (lv) => all.filter((l) => l.level === lv).length;
  const rows = all.filter((l) => (level === "all" || l.level === level)
    && (!query || l.message.toLowerCase().includes(query.toLowerCase())));

  let body;
  if (logs.loading && !logs.data) body = html`<${Loading} />`;
  else if (logs.error && !logs.data) body = html`<${ErrorBox} error=${logs.error} onRetry=${logs.reload} />`;
  else if (!rows.length) body = html`<${Empty} icon="logs" title="No log entries">${all.length ? "Nothing matches the filter." : "Relay events will show up here."}<//>`;
  else {
    body = html`<div class="table-wrap"><table class="log-table">
      <thead><tr><th>Time</th><th>Level</th><th>Event</th></tr></thead>
      <tbody>${rows.map((l) => html`<tr>
        <td class="nowrap muted">${dateTime(l.ts)}</td>
        <td><${Status} tone=${TONE[l.level] || "info"}>${l.level}<//></td>
        <td class="log-msg">${l.message}</td>
      </tr>`)}</tbody>
    </table></div>`;
  }

  return html`
    <${PageHeader} title="Logs" description="Relay events of the last 7 days: machines connecting, bind failures, rejected handshakes." />
    <${Card} flush>
      <div class="toolbar">
        <${Tabs} value=${level} onChange=${setLevel} tabs=${[
          { value: "all", label: "All", count: all.length },
          { value: "info", label: "Info", count: count("info") },
          { value: "warn", label: "Warnings", count: count("warn") },
          { value: "error", label: "Errors", count: count("error") },
        ]} />
        <${Input} type="search" class="input search" placeholder="Search events" value=${query} onInput=${setQuery} />
      </div>
      ${body}
    <//>`;
}
