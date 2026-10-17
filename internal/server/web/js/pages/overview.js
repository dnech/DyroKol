// Dashboard: headline numbers, traffic chart, issues and the flow map.
import { html, useMemo, useState } from "../lib/html.js";
import { api } from "../lib/api.js";
import { usePolled } from "../lib/store.js";
import { bytes, compact, rate } from "../lib/format.js";
import { navigate } from "../lib/router.js";
import { issues, useApp } from "../state.js";
import { bucketize, TrafficChart } from "../charts/traffic.js";
import { RouteMap } from "../charts/routemap.js";
import { useMapLayout } from "../lib/maplayout.js";
import { Icon } from "../ui/icons.js";
import { Card, Empty, PageHeader, Select, StatTile, Status, Button } from "../ui/kit.js";

const WINDOW = 3600;
const BUCKET = 60;

export function OverviewPage() {
  const { status } = useApp();
  const { layout } = useMapLayout();
  const [port, setPort] = useState("all");
  const stats = usePolled(() => api.stats(Math.floor(Date.now() / 1000) - WINDOW), 10000);

  const points = useMemo(() => {
    const samples = (stats.data?.samples || []).filter((s) => port === "all" || String(s.inport_id) === port);
    return bucketize(samples, Date.now() / 1000, WINDOW, BUCKET);
  }, [stats.data, port]);

  const online = status.clients.filter((c) => c.online).length;
  const problems = issues(status);
  // the last bucket is still filling up: report the previous, complete minute
  const current = points[points.length - 2] || { in: 0, out: 0 };

  return html`
    <${PageHeader} title="Overview" description="Live state of the relay and every machine connected to it." />
    <div class="stats-grid">
      <${StatTile} label="Machines online" value=${`${online} / ${status.clients.length}`}
        sub=${online === status.clients.length ? "all connected" : `${status.clients.length - online} offline`} />
      <${StatTile} label="Active connections" value=${compact(status.totals.active)}
        sub=${`${compact(status.totals.conns_total)} served in total`} />
      <${StatTile} label="Throughput · last minute" value=${rate(current.in + current.out)}
        sub=${`in ${rate(current.in)} · out ${rate(current.out)}`} />
      <${StatTile} label="Transferred" value=${bytes(status.totals.bytes_in + status.totals.bytes_out)}
        sub=${`in ${bytes(status.totals.bytes_in)} · out ${bytes(status.totals.bytes_out)}`} />
    </div>

    <${Card} title="Map" actions=${html`<${Button} size="sm" variant="ghost" onClick=${() => navigate("routes")}>Edit routes<//>`}>
      <${RouteMap} status=${status} layout=${layout} />
    <//>

    <${Card} title="Needs attention" flush>
      ${problems.length ? html`<ul class="issue-list">
        ${problems.map((p) => html`<li>
          <button type="button" class="issue" onClick=${() => navigate(p.page)}>
            <${Status} tone=${p.level}>${p.level}<//>
            <span class="issue-text">${p.text}<small>${p.detail}</small></span>
            <${Icon} name="arrow" />
          </button>
        </li>`)}
      </ul>` : html`<${Empty} icon="circleCheck" title="All good">Every port is bound and routed, every machine is online.<//>`}
    <//>

    <${Card} title="Throughput · last hour" actions=${html`
      <${Select} value=${port} onChange=${setPort} aria-label="Ingress port"
        options=${[{ value: "all", label: "All ingress ports" },
          ...status.inports.map((p) => ({ value: String(p.id), label: `${p.machine_label}:${p.port}${p.label ? ` — ${p.label}` : ""}` }))]} />`}>
      <${TrafficChart} points=${points} />
    <//>`;
}
