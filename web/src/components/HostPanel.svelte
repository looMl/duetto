<script lang="ts">
  import { onDestroy } from 'svelte';
  import Icon from './Icon.svelte';
  import type { Session } from '../lib/session.svelte';
  import type { Variant } from '../lib/protocol';
  import { formatBitrate, formatDuration, joinNames } from '../lib/format';

  let { session, variant, onclose }: { session: Session; variant: Variant | null; onclose: () => void } = $props();

  // Mirrors app.status (Go).
  interface Status {
    link: string;
    tunnel: { state: 'off' | 'starting' | 'ready' | 'error'; error?: string };
    library: { path: string; name: string; size: number }[];
    current: string;
    prepare: {
      state: 'idle' | 'running' | 'done' | 'stopped' | 'error';
      step?: string;
      stepIndex: number;
      stepCount: number;
      progress: number;
      eta: number;
      error?: string;
      pending: { id: string; label: string }[] | null;
    };
  }

  let status = $state<Status | null>(null);
  let copied = $state(false);
  let error = $state('');
  let busy = $state(false);
  let uploading = $state(false);
  let dragging = $state(false);
  let fileInput = $state<HTMLInputElement>();
  const SUB_EXT = /\.(srt|vtt|ass|ssa)$/i;

  const prep = $derived(status?.prepare);
  const running = $derived(prep?.state === 'running');
  const pending = $derived(prep?.pending ?? []);
  const cantPlay = $derived(session.viewers.filter((v) => v.unsupported).map((v) => v.name));
  const currentName = $derived((status?.library ?? []).find((f) => f.path === status?.current)?.name ?? '');

  async function refresh() {
    try {
      const r = await fetch('/api/admin/status');
      if (r.ok) status = (await r.json()) as Status;
    } catch { /* transient */ }
  }

  async function action(name: string, body: unknown = {}) {
    error = '';
    busy = true;
    try {
      const r = await fetch(`/api/admin/${name}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      });
      if (!r.ok) error = ((await r.json().catch(() => ({}))) as { error?: string }).error ?? 'Failed';
      await refresh();
    } finally {
      busy = false;
    }
  }

  async function upload(file: File | undefined) {
    if (!file) return;
    error = '';
    if (!SUB_EXT.test(file.name)) { error = 'Use an .srt, .vtt, .ass or .ssa file.'; return; }
    uploading = true;
    try {
      const r = await fetch('/api/admin/subtitles', {
        method: 'POST', body: file, headers: { 'X-Filename': encodeURIComponent(file.name) },
      });
      if (!r.ok) error = ((await r.json().catch(() => ({}))) as { error?: string }).error ?? 'Upload failed';
    } catch {
      error = 'Upload failed';
    } finally {
      uploading = false;
      if (fileInput) fileInput.value = '';
    }
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    dragging = false;
    upload(e.dataTransfer?.files[0]);
  }

  async function copy() {
    if (!status?.link) return;
    await navigator.clipboard.writeText(status.link);
    copied = true;
    setTimeout(() => (copied = false), 1500);
  }

  refresh();
  const timer = setInterval(refresh, 1000);
  onDestroy(() => clearInterval(timer));

  const gb = (n: number) => `${(n / 1e9).toFixed(1)} GB`;
</script>

<aside aria-label="Host" class:dragging
  ondragover={(e) => { e.preventDefault(); dragging = true; }}
  ondragleave={(e) => { if (e.currentTarget === e.target) dragging = false; }}
  ondrop={onDrop}>
  <div class="head">
    <h2>Host</h2>
    <button class="close" onclick={onclose} aria-label="Close"><Icon name="x" size={18} /></button>
  </div>

  <section>
    <h3>Invite</h3>
    {#if status?.tunnel.state === 'ready' && status.link}
      <div class="link">
        <input readonly value={status.link} aria-label="Invite link" onfocus={(e) => e.currentTarget.select()} />
        <button class="btn" onclick={copy}><Icon name={copied ? 'check' : 'link'} size={16} />{copied ? 'Copied' : 'Copy'}</button>
      </div>
      <p class="hint">Anyone with this link can join. <button class="text" onclick={() => action('rotate')}>Create a new link</button> to revoke it.</p>
    {:else if status?.tunnel.state === 'starting'}
      <p class="hint row"><span class="spinner small"></span>Opening a secure tunnel…</p>
    {:else if status?.tunnel.state === 'error'}
      <p class="hint err">{status.tunnel.error}</p>
      <button class="btn ghost" onclick={() => action('tunnel')}>Retry</button>
    {:else}
      <button class="btn ghost" onclick={() => action('tunnel')}>Go online</button>
    {/if}
  </section>

  <section>
    <h3>Watching</h3>
    <ul class="viewers">
      {#each session.viewers as v (v.id)}
        <li>
          <i class:warn={v.buffering} class:bad={v.unsupported}></i>
          <span class="name">{v.name}{v.id === session.you ? ' (you)' : ''}</span>
          <span class="meta">
            {#if v.unsupported}can't play this version{:else}{Math.round(v.ahead)}s ready · {Math.round(v.drift * 1000)} ms{/if}
          </span>
        </li>
      {/each}
    </ul>
    {#if variant}<p class="hint">You: {variant.label} · {formatBitrate(variant.bitrate)}</p>{/if}
  </section>

  {#if status?.current}
    <section>
      <h3>Now showing</h3>
      <div class="card">
        <p class="file" title={currentName}>{currentName}</p>

        {#if running && prep}
          <div class="step">
            <span class="name">
              {#if prep.stepCount > 1}<span class="muted">Step {prep.stepIndex} of {prep.stepCount} · </span>{/if}{prep.step}
            </span>
            <button class="btn ghost small" onclick={() => action('stop')} disabled={busy}>Stop</button>
          </div>
          {#if prep.stepIndex > 0}
            <div class="bar"><div style:width="{prep.progress * 100}%"></div></div>
            <p class="hint">
              {Math.floor(prep.progress * 100)}%{#if prep.eta >= 0}{` · ${formatDuration(prep.eta)} left`}{/if}
            </p>
          {/if}
        {:else if prep?.state === 'stopped'}
          <p class="hint">Stopped. Versions already finished are still playable.</p>
        {:else if prep?.state === 'error'}
          <p class="hint err">{prep.error}</p>
        {/if}

        {#if session.media?.variants.length}
          <ul class="versions">
            {#each session.media.variants as v (v.id)}
              <li><Icon name="check" size={14} /><span class="name">{v.label}</span><span class="meta">{formatBitrate(v.bitrate)}</span></li>
            {/each}
          </ul>
        {/if}

        <div class="subs">
          <div class="subs-head">
            <span>Subtitles</span>
            <button class="text" onclick={() => fileInput?.click()} disabled={uploading}>
              {uploading ? 'Adding…' : 'Add subtitles…'}
            </button>
            <input bind:this={fileInput} type="file" accept=".srt,.vtt,.ass,.ssa" hidden
              onchange={(e) => upload(e.currentTarget.files?.[0])} />
          </div>
          {#if session.media?.subs.length}
            <ul class="versions">
              {#each session.media.subs as sub (sub.id)}
                <li><Icon name="cc" size={14} /><span class="name">{sub.label}</span></li>
              {/each}
            </ul>
          {:else}
            <p class="hint">None yet.</p>
          {/if}
          {#if session.media?.imageSubs}
            <p class="hint">{session.media.imageSubs} picture-based track{session.media.imageSubs === 1 ? '' : 's'} in this file can't be shown in a browser. Add an .srt instead, or drop one here.</p>
          {/if}
        </div>

        {#if pending.length && !running}
          {#if cantPlay.length}
            <p class="hint warn">{joinNames(cantPlay)} can't play this version. Make a compatible copy:</p>
          {:else}
            <p class="hint">Only needed if someone's browser can't play the original. Encodes on your GPU; can take a while.</p>
          {/if}
          {#each pending as p (p.id)}
            <button class="btn ghost full" onclick={() => action('compat')} disabled={busy}>Make {p.label} copy</button>
          {/each}
        {/if}
      </div>
    </section>
  {/if}

  <section>
    <h3>Library</h3>
    <ul class="library">
      {#each status?.library ?? [] as f (f.path)}
        <li>
          <button class:active={f.path === status?.current} disabled={busy}
            onclick={() => action('open', { path: f.path })} title={f.name}>
            <span class="name">{f.name}</span><span class="meta">{gb(f.size)}</span>
          </button>
        </li>
      {:else}
        <li class="hint">No videos found in the library folders.</li>
      {/each}
    </ul>
  </section>

  {#if error}<p class="hint err" role="alert">{error}</p>{/if}
</aside>

<style>
  aside {
    position: absolute; top: 0; right: 0; bottom: 0; z-index: 4; width: min(380px, 100%);
    padding: 18px; overflow-y: auto; overflow-x: hidden; background: rgba(12, 12, 12, 0.97);
    border-left: 1px solid var(--line); backdrop-filter: blur(16px); user-select: text;
    /* minmax(0, 1fr): long file names must not widen the column. */
    display: grid; grid-template-columns: minmax(0, 1fr); align-content: start; gap: 22px;
  }
  section { min-width: 0; }
  aside.dragging { outline: 2px dashed var(--muted); outline-offset: -8px; }
  .subs { margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--line); }
  .subs-head { display: flex; justify-content: space-between; align-items: center; font-size: 13px; }
  .subs-head .text:disabled { color: var(--muted); text-decoration: none; cursor: default; }

  .head { display: flex; align-items: center; justify-content: space-between; }
  h2 { font-size: 16px; font-weight: 600; margin: 0; }
  h3 { font-size: 12px; font-weight: 500; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; margin: 0 0 10px; }
  .close { width: 32px; height: 32px; display: grid; place-items: center; border-radius: 8px; color: var(--muted); }
  .close:hover { background: var(--surface-2); color: var(--text); }

  .link { display: flex; gap: 8px; }
  .link input {
    flex: 1; min-width: 0; height: 40px; padding: 0 12px; border-radius: 8px;
    border: 1px solid var(--line); background: var(--surface); color: var(--muted); font-size: 13px;
  }
  .hint { font-size: 13px; color: var(--muted); margin: 8px 0 0; }
  .hint.err, .err { color: var(--danger); }
  .hint.warn { color: var(--warn); }
  .row { display: flex; align-items: center; gap: 10px; }
  .text { color: var(--text); text-decoration: underline; text-underline-offset: 2px; }
  .spinner.small { width: 16px; height: 16px; }
  .btn.small { height: 30px; padding: 0 12px; font-size: 13px; flex-shrink: 0; }
  .btn.full { width: 100%; margin-top: 10px; }

  ul { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; }
  .name { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .meta { color: var(--faint); font-size: 12px; white-space: nowrap; flex-shrink: 0; }

  .viewers li { display: flex; align-items: center; gap: 10px; padding: 6px 0; font-size: 14px; }
  .viewers .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .viewers i { width: 7px; height: 7px; border-radius: 50%; background: var(--ok); flex-shrink: 0; }
  .viewers i.warn { background: var(--warn); }
  .viewers i.bad { background: var(--danger); }

  .card { padding: 12px; border: 1px solid var(--line); border-radius: var(--radius); background: var(--surface); }
  .file {
    margin: 0; font-size: 14px; overflow-wrap: anywhere;
    display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  }
  .step { display: flex; align-items: center; gap: 10px; margin-top: 12px; font-size: 13px; }
  .bar { height: 3px; border-radius: 3px; background: var(--surface-2); overflow: hidden; margin-top: 10px; }
  .bar div { height: 100%; background: var(--accent); transition: width 0.4s var(--ease); }
  .versions { margin-top: 10px; }
  .versions li { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--muted); padding: 3px 0; }
  .versions li :global(svg) { color: var(--ok); flex-shrink: 0; }
  .subs .versions li :global(svg) { color: var(--muted); }

  .library button {
    width: 100%; display: flex; gap: 10px; align-items: center; padding: 9px 10px; border-radius: 8px;
    text-align: left; font-size: 14px; border: 1px solid transparent;
  }
  .library .name {
    display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  }
  .library button:hover:not(:disabled) { background: var(--surface-2); }
  .library button.active { border-color: var(--line); background: var(--surface); }
  .library button:disabled { opacity: 0.5; cursor: default; }
</style>
