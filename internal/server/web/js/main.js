// Entry point: renders the login screen or the app shell.
import { html, render } from "./lib/html.js";
import { start, useApp } from "./state.js";
import { Shell } from "./shell.js";
import { LoginPage } from "./pages/login.js";
import { DialogHost } from "./ui/dialog.js";
import { Toasts } from "./ui/toast.js";

function App() {
  const { authed } = useApp();
  return html`
    ${authed ? html`<${Shell} />` : html`<${LoginPage} />`}
    <${DialogHost} />
    <${Toasts} />`;
}

document.documentElement.dataset.theme = localStorage.getItem("dyrokol_theme")
  || (matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark");
render(html`<${App} />`, document.getElementById("app"));
start();
