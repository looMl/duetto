<script lang="ts">
  import Join from './components/Join.svelte';
  import Player from './components/Player.svelte';
  import { Session } from './lib/session.svelte';

  type Phase =
    | { kind: 'loading' }
    | { kind: 'join'; key: string }
    | { kind: 'enter'; name: string } // has a session; needs one click (autoplay policy)
    | { kind: 'invalid' }
    | { kind: 'watching' };

  let phase = $state<Phase>({ kind: 'loading' });
  const session = new Session();

  function keyFromHash(): string {
    return new URLSearchParams(location.hash.slice(1)).get('k') ?? '';
  }

  async function init() {
    const res = await fetch('/api/session', { credentials: 'same-origin' }).catch(() => null);
    const s = res?.ok ? ((await res.json()) as { joined: boolean; name: string }) : null;
    if (s?.joined) {
      phase = { kind: 'enter', name: s.name };
      return;
    }
    const key = keyFromHash();
    phase = key ? { kind: 'join', key } : { kind: 'invalid' };
  }

  function start() {
    // The key has done its job; keep it out of the address bar and history.
    if (location.hash) history.replaceState(null, '', location.pathname);
    session.connect();
    phase = { kind: 'watching' };
  }

  init();
</script>

{#if phase.kind === 'loading'}
  <div class="center"><div class="spinner"></div></div>
{:else if phase.kind === 'join'}
  <Join key={phase.key} onjoined={start} />
{:else if phase.kind === 'enter'}
  <Join name={phase.name} onjoined={start} />
{:else if phase.kind === 'invalid'}
  <div class="center">
    <div class="card">
      <h1>Link incomplete</h1>
      <p class="muted">Ask for a new invite link and open it exactly as you received it.</p>
    </div>
  </div>
{:else}
  <Player {session} />
{/if}

<style>
  .center { height: 100%; display: grid; place-items: center; padding: 24px; }
  .card { max-width: 360px; text-align: center; }
  h1 { font-size: 20px; font-weight: 600; margin: 0 0 8px; }
  p { margin: 0; }
</style>
