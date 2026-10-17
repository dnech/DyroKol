// Login: the admin UI token unlocks the API for this browser tab.
import { html, useState } from "../lib/html.js";
import { login } from "../state.js";
import { Button, Field, FormError, Input } from "../ui/kit.js";
import { Brand } from "../shell.js";

export function LoginPage() {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (e) => {
    e.preventDefault();
    if (!token.trim()) return;
    setBusy(true);
    setError("");
    try {
      await login(token.trim());
    } catch (err) {
      setError(err.status === 401 ? "This token is not valid for this relay." : err.message);
      setBusy(false);
    }
  };
  return html`<div class="login-screen">
    <form class="login-card" onSubmit=${submit}>
      <${Brand} large />
      <p class="login-text">Enter the admin UI token. It is printed in the relay log
        (<code>admin api token</code>) and by admin machines at startup (<code>admin ui token</code>).</p>
      <${FormError} error=${error} />
      <${Field} label="Admin token">
        <${Input} type="password" value=${token} onInput=${setToken} autocomplete="off" autofocus required />
      <//>
      <${Button} type="submit" variant="primary" busy=${busy} class="btn-block">Unlock<//>
      <p class="field-hint">The token is kept only for this browser tab.</p>
    </form>
  </div>`;
}
