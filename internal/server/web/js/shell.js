// App frame: sidebar navigation, top bar and the routed page.
import { html, useEffect, useState } from "./lib/html.js";
import { navigate, useRoute } from "./lib/router.js";
import { ago } from "./lib/format.js";
import { issues, logout, refresh, useApp } from "./state.js";
import { Icon } from "./ui/icons.js";
import { ErrorBox, IconButton, Loading } from "./ui/kit.js";
import { OverviewPage } from "./pages/overview.js";
import { MachinesPage } from "./pages/machines.js";
import { IngressPage } from "./pages/ingress.js";
import { EgressPage } from "./pages/egress.js";
import { RoutesPage } from "./pages/routes.js";
import { ActivityPage } from "./pages/activity.js";
import { LogsPage } from "./pages/logs.js";
import { SettingsPage } from "./pages/settings.js";

const PAGES = [
  { id: "overview", label: "Overview", icon: "overview", page: OverviewPage },
  { id: "machines", label: "Machines", icon: "machines", page: MachinesPage },
  { id: "ingress", label: "Ingress ports", icon: "ingress", page: IngressPage },
  { id: "egress", label: "Egress ports", icon: "egress", page: EgressPage },
  { id: "routes", label: "Routes", icon: "route", page: RoutesPage },
  { id: "activity", label: "Activity", icon: "activity", page: ActivityPage },
  { id: "logs", label: "Logs", icon: "logs", page: LogsPage },
  { id: "settings", label: "Settings", icon: "settings", page: SettingsPage },
];

export function Brand({ large }) {
  return html`<div class=${`brand${large ? " brand-large" : ""}`}>
    <img src="icon.svg" alt="" width=${large ? 36 : 28} height=${large ? 36 : 28} />
    <span class="brand-text">DyroKol<small>Hole Punch</small></span>
  </div>`;
}

// --- theme ---

const THEME_KEY = "dyrokol_theme";

function useTheme() {
  const [theme, setTheme] = useState(() => localStorage.getItem(THEME_KEY)
    || (matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark"));
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem(THEME_KEY, theme);
  }, [theme]);
  return [theme, () => setTheme(theme === "dark" ? "light" : "dark")];
}

// Freshness re-renders every second so "updated Ns ago" stays current.
function Freshness({ updatedAt, error }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, []);
  if (error) {
    return html`<button type="button" class="freshness stale" onClick=${refresh} title=${error.message}>
      <span class="pulse" />Relay unreachable · retry
    </button>`;
  }
  return html`<button type="button" class="freshness" onClick=${refresh} title="Refresh now">
    <span class="pulse live" />Updated ${ago(updatedAt / 1000)}
  </button>`;
}

export function Shell() {
  const route = useRoute();
  const { status, error, updatedAt } = useApp();
  const [theme, toggleTheme] = useTheme();
  const [navOpen, setNavOpen] = useState(false);
  const serverName = status?.name || "";
  useEffect(() => { document.title = serverName ? `${serverName} · DyroKol` : "DyroKol"; }, [serverName]);
  const current = PAGES.find((p) => p.id === route) || PAGES[0];

  useEffect(() => { setNavOpen(false); document.title = `${current.label} · DyroKol`; }, [current]);

  const counts = status && {
    machines: `${status.clients.filter((c) => c.online).length}/${status.clients.length}`,
    ingress: status.inports.length,
    routes: status.routes.length,
    egress: status.endpoints.length,
    overview: issues(status).length || null,
  };

  const Page = current.page;
  return html`<div class=${`shell${navOpen ? " nav-open" : ""}`}>
    <aside class="sidebar">
      <div class="sidebar-head"><${Brand} /></div>
      <nav class="nav">
        ${PAGES.map((p) => html`<a href=${`#/${p.id}`} class=${`nav-item${p.id === current.id ? " active" : ""}`}
            aria-current=${p.id === current.id ? "page" : undefined}>
          <${Icon} name=${p.icon} size=${18} />
          <span>${p.label}</span>
          ${counts?.[p.id] != null && html`<span class=${`nav-count${p.id === "overview" ? " nav-alert" : ""}`}>${counts[p.id]}</span>`}
        </a>`)}
      </nav>
      <div class="sidebar-foot">
        ${status && html`<div class="mono muted" title=${status.fingerprint}>v${status.version} · ${status.fingerprint.slice(0, 19)}…</div>`}
      </div>
    </aside>
    <div class="scrim" onClick=${() => setNavOpen(false)} />
    <div class="main">
      <header class="topbar">
        <${IconButton} icon="menu" label="Menu" class="icon-btn menu-btn" onClick=${() => setNavOpen(!navOpen)} />
        ${serverName && html`<div class="server-name" title="This relay's name (Settings)">${serverName}</div>`}
        <${Freshness} updatedAt=${updatedAt} error=${error} />
        <div class="topbar-actions">
          <${IconButton} icon=${theme === "dark" ? "sun" : "moon"} label="Toggle theme" onClick=${toggleTheme} />
          <${IconButton} icon="lock" label="Lock" onClick=${logout} />
        </div>
      </header>
      <main class="content">
        ${status ? html`<${Page} />`
          : error ? html`<${ErrorBox} error=${error} onRetry=${refresh} />` : html`<${Loading} />`}
      </main>
    </div>
  </div>`;
}

export { navigate };
