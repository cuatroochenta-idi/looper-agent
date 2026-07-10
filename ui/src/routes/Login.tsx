import { createResource, createSignal, Show } from "solid-js";
import { api } from "../lib/api";
import { withBase } from "../lib/base";
import { Input } from "../components/ui/Input";
import { Button } from "../components/ui/Button";
import { Spinner } from "../components/ui/Spinner";

export function Login() {
  const [me] = createResource(() => api.me());
  const [username, setUsername] = createSignal("");
  const [password, setPassword] = createSignal("");
  const [error, setError] = createSignal("");
  const [busy, setBusy] = createSignal(false);

  const nextUrl = () => new URLSearchParams(window.location.search).get("next") || withBase("/");

  const submit = async (e: Event) => {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      await api.login(password(), me()?.username !== undefined ? username() || undefined : undefined);
      window.location.assign(nextUrl());
    } catch (err) {
      setError(err instanceof Error && /401/.test(err.message) ? "Invalid credentials." : "Sign-in failed. Try again.");
    } finally {
      setBusy(false);
    }
  };

  const needsUsername = () => me()?.auth_enabled && me()?.username !== undefined;

  return (
    <div class="relative flex min-h-screen items-center justify-center overflow-hidden bg-bg px-4">
      <div class="bg-grid pointer-events-none absolute inset-0" />
      <div class="pointer-events-none absolute left-1/2 top-1/3 h-72 w-72 -translate-x-1/2 rounded-full bg-accent/20 blur-[120px]" />

      <form
        onSubmit={submit}
        class="relative w-full max-w-[340px] rounded-[16px] border border-line bg-card p-6 shadow-[var(--shadow-pop)] fade-up"
      >
        <div class="mb-5 flex items-center gap-2">
          <span class="relative flex h-2.5 w-2.5">
            <span class="absolute inset-0 rounded-full bg-accent" />
            <span class="pulse absolute inset-0 rounded-full bg-accent shadow-[0_0_10px_2px_var(--accent)]" />
          </span>
          <span class="text-[16px] font-semibold tracking-tight text-text">
            Looper<span class="text-accent">Agent</span>
          </span>
        </div>

        <h1 class="text-[14px] font-semibold text-text">Sign in</h1>
        <p class="mt-0.5 mb-4 text-[12px] text-muted">Enter the panel password to continue.</p>

        <Show when={me.loading}>
          <div class="flex justify-center py-6"><Spinner /></div>
        </Show>

        <Show when={!me.loading}>
          <Show when={needsUsername()}>
            <label class="mb-1 block text-[11.5px] font-medium text-muted">username</label>
            <Input
              value={username()}
              onInput={(e) => setUsername(e.currentTarget.value)}
              autocomplete="username"
              class="mb-3"
            />
          </Show>

          <label class="mb-1 block text-[11.5px] font-medium text-muted">password</label>
          <Input
            type="password"
            value={password()}
            onInput={(e) => setPassword(e.currentTarget.value)}
            autocomplete="current-password"
            autofocus
            class="mb-3"
          />

          <Show when={error()}>
            <div class="mb-3 rounded-[8px] border border-danger/30 bg-danger-soft px-3 py-2 text-[12px] text-danger">
              {error()}
            </div>
          </Show>

          <Button type="submit" variant="primary" class="w-full" disabled={busy() || !password()}>
            <Show when={busy()} fallback="Sign in"><Spinner size={13} /> Signing in…</Show>
          </Button>

          <Show when={me() && !me()!.auth_enabled}>
            <p class="mt-3 text-center text-[11px] text-faint">Auth is disabled — any value continues.</p>
          </Show>
        </Show>
      </form>
    </div>
  );
}
