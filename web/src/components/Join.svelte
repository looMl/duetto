<script lang="ts">
  // key: first visit, trade the invite key for a session. name: returning
  // viewer; the click satisfies the autoplay policy so remote play has sound.
  let { key = '', name = '', onjoined }: { key?: string; name?: string; onjoined: () => void } = $props();

  const NAME_KEY = 'duetto.name';
  let input = $state(readName());
  let busy = $state(false);
  let error = $state('');

  function readName(): string {
    try { return localStorage.getItem(NAME_KEY) ?? ''; } catch { return ''; }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!key) { onjoined(); return; }
    busy = true;
    error = '';
    try {
      const res = await fetch('/api/join', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ key, name: input }),
        credentials: 'same-origin',
      });
      const body = (await res.json().catch(() => ({}))) as { error?: string };
      if (!res.ok) { error = body.error ?? 'Could not join'; return; }
      try { localStorage.setItem(NAME_KEY, input.trim()); } catch { /* private mode */ }
      onjoined();
    } catch {
      error = 'Network error. Check your connection and try again.';
    } finally {
      busy = false;
    }
  }
</script>

<main>
  <form onsubmit={submit}>
    <div class="mark" aria-hidden="true"><span></span><span></span></div>
    {#if key}
      <h1>You're invited to watch together</h1>
      <p class="muted">Playback stays in sync for everyone.</p>
      <label class="sr-only" for="name">Your name</label>
      <!-- svelte-ignore a11y_autofocus -->
      <input id="name" class="field" placeholder="Your name" autocomplete="nickname"
        maxlength="32" bind:value={input} autofocus required />
      <button class="btn" type="submit" disabled={busy || !input.trim()}>
        {busy ? 'Joining…' : 'Join'}
      </button>
    {:else}
      <h1>Welcome back{name ? `, ${name}` : ''}</h1>
      <p class="muted">Ready when you are.</p>
      <!-- svelte-ignore a11y_autofocus -->
      <button class="btn" type="submit" autofocus>Enter</button>
    {/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  </form>
</main>

<style>
  main { height: 100%; display: grid; place-items: center; padding: 24px; }
  form { width: 100%; max-width: 340px; display: grid; gap: 12px; text-align: center; }
  h1 { font-size: 20px; font-weight: 600; margin: 8px 0 0; letter-spacing: -0.01em; }
  p { margin: 0 0 12px; }
  .btn { width: 100%; height: 44px; }
  .error { color: var(--danger); margin: 0; font-size: 14px; }
  .mark { display: flex; justify-content: center; margin-bottom: 8px; }
  .mark span { width: 22px; height: 22px; border-radius: 50%; border: 1.5px solid var(--text); }
  .mark span:first-child { background: var(--text); margin-right: -7px; }
</style>
