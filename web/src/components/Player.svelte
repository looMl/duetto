<script lang="ts">
  import { onDestroy } from 'svelte';
  import Icon from './Icon.svelte';
  import Seekbar from './Seekbar.svelte';
  import HostPanel from './HostPanel.svelte';
  import type { Session } from '../lib/session.svelte';
  import type { Variant } from '../lib/protocol';
  import { bufferedAhead, correct, isBuffering, localNow, targetPosition } from '../lib/sync';
  import { pickVariant, rankVariants, type VariantSupport } from '../lib/variants';
  import { formatTime, joinNames } from '../lib/format';

  let { session }: { session: Session } = $props();

  let root: HTMLDivElement;
  let video = $state<HTMLVideoElement>();
  let ranked = $state<VariantSupport[]>([]);
  let manualVariant = $state<string | null>(null);
  // Versions this browser failed to decode (capability checks can be optimistic).
  let failedVariants = $state<string[]>([]);
  let cantDecode = $state(false);
  const usable = $derived(ranked.filter((r) => !failedVariants.includes(r.variant.id)));
  let variant = $derived<Variant | null>(
    (manualVariant && ranked.find((r) => r.variant.id === manualVariant)?.variant) ||
      pickVariant(usable.length ? usable : ranked),
  );
  let subId = $state<string>(readPref('duetto.subs') ?? '');

  // UI state mirrored from the <video> element.
  let current = $state(0);
  let buffered = $state(0);
  let volume = $state(Number(readPref('duetto.volume') ?? 1));
  let muted = $state(false);
  let fullscreen = $state(false);
  let controlsVisible = $state(true);
  let menu = $state<'none' | 'subs' | 'quality'>('none');
  let hostPanel = $state(false);
  let needsGesture = $state(false);
  let mediaError = $state('');
  let toast = $state('');
  let now = $state(localNow()); // ticks for countdowns

  const duration = $derived(session.media?.duration ?? 0);
  const st = $derived(session.state);
  const intendsPlay = $derived(!st.paused || (st.waiting?.length ?? 0) > 0);
  const others = $derived(session.viewers.filter((v) => v.id !== session.you));
  const countdown = $derived(!st.paused && st.at > session.clock.serverNow(now) ? Math.ceil((st.at - session.clock.serverNow(now)) / 1000) : 0);

  // The host lands on an empty room: show where to pick a movie and the link.
  $effect(() => {
    if (session.host && session.connected && !session.media) hostPanel = true;
  });

  // ---------- variant selection ----------
  let rankedFor = '';
  $effect(() => {
    const m = session.media;
    if (!m) { ranked = []; return; }
    if (m.id !== rankedFor) {
      // Variant ids repeat across movies ("original"): forget per-movie choices.
      rankedFor = m.id;
      failedVariants = [];
      manualVariant = null;
    }
    let cancelled = false;
    rankVariants(m.variants).then((r) => { if (!cancelled) ranked = r; });
    return () => { cancelled = true; };
  });

  // ---------- sync engine ----------
  let waitingSince = 0;
  let lastSeekAt = 0;
  let seekLatency = 0.3; // seconds, learned from 'seeking'→'seeked'
  let seekStartedAt = 0;
  let startTimer: ReturnType<typeof setTimeout> | undefined;
  let lastReport = { buffering: false, at: 0 };
  let drift = 0;

  function apply() {
    const v = video;
    if (!v || !variant || !session.clock.ready || v.readyState === 0) return;
    const server = session.clock.serverNow();
    const s = session.state;
    const target = clampTime(targetPosition(s, server));
    const shouldPlay = !s.paused && server >= s.at;

    if (!shouldPlay) {
      if (!v.paused) v.pause();
      v.playbackRate = 1;
      drift = 0;
      if (Math.abs(v.currentTime - target) > 0.05 && !v.seeking) v.currentTime = target;
      clearTimeout(startTimer);
      if (!s.paused) startTimer = setTimeout(apply, Math.max(0, s.at - server));
      return;
    }
    if (v.ended || target >= duration - 0.05) return;

    if (v.paused) {
      if (Math.abs(v.currentTime - target) > 0.1) v.currentTime = target;
      play();
      return;
    }
    if (v.seeking || v.readyState < 3) return;

    drift = v.currentTime - target;
    const c = correct(drift, target);
    if (c.kind === 'seek') {
      if (performance.now() - lastSeekAt < 3000) return; // let the last seek land
      lastSeekAt = performance.now();
      v.currentTime = clampTime(c.to + seekLatency); // land where the room will be
      v.playbackRate = 1;
    } else {
      v.playbackRate = c.kind === 'rate' ? c.rate : 1;
    }
  }

  async function play() {
    try {
      await video?.play();
      needsGesture = false;
    } catch (e) {
      if ((e as DOMException).name === 'NotAllowedError') needsGesture = true;
    }
  }

  function clampTime(t: number) {
    return duration > 0 ? Math.min(Math.max(0, t), duration) : Math.max(0, t);
  }

  /** Tell the room whether we can play right now. */
  function report(force = false) {
    const v = video;
    if (!v) return;
    const ahead = bufferedAhead(v.buffered, v.currentTime);
    const s = session.state;
    const server = session.clock.serverNow();
    const shouldPlay = !s.paused && server >= s.at;
    const buffering = isBuffering({
      readyState: v.readyState,
      seeking: v.seeking,
      failed: !!mediaError || !variant,
      shouldPlay,
      stalledMs: waitingSince > 0 ? performance.now() - waitingSince : 0,
      atEnd: duration > 0 && v.currentTime > duration - 0.5,
    });

    const t = performance.now();
    if (force || buffering !== lastReport.buffering || t - lastReport.at > 1000) {
      session.send({ t: 'status', buffering, ahead, drift, unsupported: cantDecode });
      lastReport = { buffering, at: t };
    }
  }

  const loop = setInterval(() => {
    now = localNow();
    apply();
    report();
  }, 250);

  $effect(() => {
    void session.stateVersion; // react to every authoritative update
    apply();
    report(true);
  });

  // Announce subtitle tracks added during the session (e.g. uploaded by the host).
  let knownSubs: Set<string> | null = null;
  let knownItem = '';
  $effect(() => {
    const m = session.media;
    if (!m) return;
    const ids = new Set(m.subs.map((s) => s.id));
    if (knownSubs && knownItem === m.id) {
      const added = m.subs.filter((s) => !knownSubs!.has(s.id));
      if (added.length) flash(`Subtitles added: ${added.map((s) => s.label).join(', ')}`);
    }
    knownSubs = ids;
    knownItem = m.id;
  });

  // Toasts for remote actions.
  let lastSeq = 0;
  let lastPaused = true;
  let lastPos = 0;
  $effect(() => {
    const s = session.state;
    if (s.seq === lastSeq) return;
    const first = lastSeq === 0;
    if (!first && s.by && s.by !== session.name) {
      if (Math.abs(s.position - lastPos) > 2 && s.paused === lastPaused) flash(`${s.by} jumped to ${formatTime(s.position)}`);
      else if (s.paused && !lastPaused) flash(`${s.by} paused`);
      else if (!s.paused && lastPaused) flash(`${s.by} pressed play`);
    }
    lastSeq = s.seq;
    lastPaused = s.paused;
    lastPos = s.position;
  });

  let toastTimer: ReturnType<typeof setTimeout> | undefined;
  function flash(text: string) {
    toast = text;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (toast = ''), 2500);
  }

  // ---------- video events ----------
  function onWaiting() { if (!waitingSince) waitingSince = performance.now(); }
  function onPlaying() { waitingSince = 0; }
  function onSeeking() { seekStartedAt = performance.now(); }
  function onSeeked() {
    if (seekStartedAt) {
      const l = (performance.now() - seekStartedAt) / 1000;
      seekLatency = Math.min(2, seekLatency * 0.6 + l * 0.4);
      seekStartedAt = 0;
    }
    apply();
  }
  function onTime() {
    if (!video) return;
    current = video.currentTime;
    buffered = Math.min(duration, current + bufferedAhead(video.buffered, current));
  }
  /** This browser can't decode the current version: remember it, switch if possible. */
  function markUndecodable() {
    cantDecode = true;
    if (variant && !failedVariants.includes(variant.id)) {
      failedVariants = [...failedVariants, variant.id]; // auto-switches if another version exists
      manualVariant = null;
    }
    mediaError = "This browser can't play this version. The host can make a compatible copy.";
  }

  function onError() {
    if (video?.error?.code === 4) { markUndecodable(); return; }
    mediaError = 'Playback error. Retrying…';
    setTimeout(() => { mediaError = ''; video?.load(); }, 2000);
  }
  function onLoaded() {
    // An undecodable video track with a playable audio track raises no error
    // in Chrome: it plays sound over black. Treat that as unsupported.
    if (video && video.videoWidth === 0) { markUndecodable(); return; }
    cantDecode = false;
    mediaError = '';
    if (video) { video.volume = volume; video.muted = muted; }
    applySubs();
    apply();
  }

  // ---------- user actions (go through the room) ----------
  function togglePlay() {
    if (needsGesture) { play(); return; }
    if (intendsPlay) {
      video?.pause(); // optimistic: feels instant, the room confirms
      session.cmd('pause');
    } else {
      session.cmd('play');
    }
  }

  function seekTo(t: number) {
    t = clampTime(t);
    if (video) video.currentTime = t; // optimistic
    current = t;
    session.cmd('seek', t);
  }

  function skip(d: number) { seekTo((video?.currentTime ?? 0) + d); }

  function setVolume(v: number) {
    volume = v;
    muted = v === 0;
    if (video) { video.volume = v; video.muted = muted; }
    writePref('duetto.volume', String(v));
  }

  function toggleMute() {
    muted = !muted;
    if (video) video.muted = muted;
  }

  function toggleFullscreen() {
    if (document.fullscreenElement) document.exitFullscreen();
    else root.requestFullscreen?.().catch(() => {});
  }

  function chooseSub(id: string) {
    subId = id;
    writePref('duetto.subs', id);
    applySubs();
    menu = 'none';
  }

  function applySubs() {
    if (!video) return;
    for (const t of Array.from(video.textTracks)) {
      t.mode = t.id === `sub-${subId}` ? 'showing' : 'disabled';
    }
  }

  function chooseVariant(id: string | null) {
    manualVariant = id;
    menu = 'none';
  }

  // ---------- controls auto-hide ----------
  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  function poke() {
    controlsVisible = true;
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      if (intendsPlay && menu === 'none' && !hostPanel) controlsVisible = false;
    }, 2500);
  }

  function onKey(e: KeyboardEvent) {
    if (e.target instanceof HTMLInputElement) return;
    const k = e.key.toLowerCase();
    const map: Record<string, () => void> = {
      ' ': togglePlay, k: togglePlay, arrowleft: () => skip(-10), arrowright: () => skip(10),
      f: toggleFullscreen, m: toggleMute,
      c: () => chooseSub(subId ? '' : (session.media?.subs[0]?.id ?? '')),
      escape: () => { menu = 'none'; hostPanel = false; },
    };
    const fn = map[k];
    if (fn) { e.preventDefault(); fn(); poke(); }
  }

  function readPref(k: string): string | null {
    try { return localStorage.getItem(k); } catch { return null; }
  }
  function writePref(k: string, v: string) {
    try { localStorage.setItem(k, v); } catch { /* private mode */ }
  }

  onDestroy(() => {
    clearInterval(loop);
    clearTimeout(startTimer);
    clearTimeout(idleTimer);
    clearTimeout(toastTimer);
    session.close();
  });
</script>

<svelte:window onkeydown={onKey} />
<svelte:document onfullscreenchange={() => (fullscreen = !!document.fullscreenElement)} />

<div class="player" class:idle={!controlsVisible} bind:this={root}
  onpointermove={poke} onpointerdown={poke} role="application" aria-label="Video player">
  {#if variant}
    <!-- svelte-ignore a11y_media_has_caption -->
    <video bind:this={video} src={`/media/${variant.id}`} preload="auto" playsinline
      onwaiting={onWaiting} onplaying={onPlaying} onseeking={onSeeking} onseeked={onSeeked}
      ontimeupdate={onTime} onprogress={onTime} onloadedmetadata={onLoaded} onerror={onError}
      onclick={togglePlay}>
      {#each session.media?.subs ?? [] as s (s.id)}
        <track id={`sub-${s.id}`} kind="subtitles" src={`/subs/${s.id}.vtt`} srclang={s.lang} label={s.label} />
      {/each}
    </video>
  {/if}

  <!-- Center status -->
  <div class="center">
    {#if !session.connected}
      <div class="status"><div class="spinner"></div><span>Reconnecting…</span></div>
    {:else if !session.media}
      <div class="status"><span class="muted">Waiting for the host to pick a movie</span></div>
    {:else if mediaError}
      <div class="status"><span>{mediaError}</span></div>
    {:else if needsGesture}
      <button class="big" onclick={() => play()} aria-label="Resume playback"><Icon name="play" size={34} /></button>
    {:else if st.waiting?.length}
      <div class="status"><div class="spinner"></div><span>Waiting for {joinNames(st.waiting)}…</span></div>
    {:else if countdown > 0}
      <div class="status"><span class="count">{countdown}</span></div>
    {:else if st.paused && session.clock.ready}
      <button class="big" onclick={togglePlay} aria-label="Play"><Icon name="play" size={34} /></button>
    {/if}
  </div>

  {#if toast}<div class="toast" role="status">{toast}</div>{/if}

  <!-- Top bar -->
  <header class="bar top">
    <div class="title">{session.media?.title ?? 'Duetto'}</div>
    <div class="people">
      {#each others as v (v.id)}
        <span class="chip" title={v.buffering ? `${v.name} is buffering` : `${v.name} is in sync`}>
          <i class:warn={v.buffering}></i>{v.name}
        </span>
      {/each}
      {#if session.host}
        <button class="icon" onclick={() => (hostPanel = !hostPanel)} aria-label="Invite and settings"><Icon name="users" /></button>
      {/if}
    </div>
  </header>

  <!-- Bottom controls -->
  <footer class="bar bottom">
    <Seekbar {duration} {current} {buffered} onseek={seekTo} />
    <div class="row">
      <button class="icon" onclick={togglePlay} aria-label={intendsPlay ? 'Pause' : 'Play'}>
        <Icon name={intendsPlay ? 'pause' : 'play'} />
      </button>
      <button class="icon" onclick={() => skip(-10)} aria-label="Back 10 seconds"><Icon name="back10" /></button>
      <button class="icon" onclick={() => skip(10)} aria-label="Forward 10 seconds"><Icon name="fwd10" /></button>
      <div class="volume">
        <button class="icon" onclick={toggleMute} aria-label={muted ? 'Unmute' : 'Mute'}>
          <Icon name={muted || volume === 0 ? 'mute' : 'volume'} />
        </button>
        <input type="range" min="0" max="1" step="0.01" value={muted ? 0 : volume}
          oninput={(e) => setVolume(Number(e.currentTarget.value))} aria-label="Volume" />
      </div>
      <span class="time">{formatTime(current)} <span class="muted">/ {formatTime(duration)}</span></span>
      <span class="spacer"></span>

      {#if (session.media?.subs.length ?? 0) > 0}
        <div class="menu-wrap">
          <button class="icon" class:on={!!subId} onclick={() => (menu = menu === 'subs' ? 'none' : 'subs')} aria-label="Subtitles" aria-expanded={menu === 'subs'}>
            <Icon name="cc" />
          </button>
          {#if menu === 'subs'}
            <div class="menu" role="menu">
              <button role="menuitemradio" aria-checked={!subId} onclick={() => chooseSub('')}>Off{#if !subId}<Icon name="check" size={16} />{/if}</button>
              {#each session.media?.subs ?? [] as s (s.id)}
                <button role="menuitemradio" aria-checked={subId === s.id} onclick={() => chooseSub(s.id)}>
                  {s.label}{#if subId === s.id}<Icon name="check" size={16} />{/if}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {/if}

      {#if ranked.length > 1}
        <div class="menu-wrap">
          <button class="icon" onclick={() => (menu = menu === 'quality' ? 'none' : 'quality')} aria-label="Quality" aria-expanded={menu === 'quality'}>
            <Icon name="settings" />
          </button>
          {#if menu === 'quality'}
            <div class="menu" role="menu">
              <button role="menuitemradio" aria-checked={!manualVariant} onclick={() => chooseVariant(null)}>
                Auto{#if !manualVariant}<Icon name="check" size={16} />{/if}
              </button>
              {#each ranked as r (r.variant.id)}
                <button role="menuitemradio" aria-checked={manualVariant === r.variant.id} disabled={!r.supported}
                  onclick={() => chooseVariant(r.variant.id)}>
                  <span>{r.variant.label}{#if !r.supported}<small> · not supported here</small>{/if}</span>
                  {#if manualVariant === r.variant.id}<Icon name="check" size={16} />{/if}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {/if}

      <button class="icon" onclick={toggleFullscreen} aria-label={fullscreen ? 'Exit full screen' : 'Full screen'}>
        <Icon name={fullscreen ? 'shrink' : 'expand'} />
      </button>
    </div>
  </footer>

  {#if hostPanel && session.host}
    <HostPanel {session} {variant} onclose={() => (hostPanel = false)} />
  {/if}
</div>

<style>
  .player { position: relative; height: 100%; background: #000; overflow: hidden; user-select: none; }
  .player.idle { cursor: none; }
  video { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; background: #000; }
  /* Keep subtitles clear of the control bar while it is visible
     (Chrome, Edge, Safari; changing cue.line doesn't redraw active cues). */
  video::-webkit-media-text-track-container {
    transition: transform 0.25s var(--ease);
    transform: translateY(-72px);
  }
  .idle video::-webkit-media-text-track-container { transform: none; }
  video::cue {
    font-family: inherit;
    font-size: clamp(16px, 2.4vw, 34px);
    line-height: 1.3;
    color: #fff;
    background: rgba(0, 0, 0, 0.55);
    text-shadow: 0 1px 2px rgba(0, 0, 0, 0.8);
  }

  .bar {
    position: absolute; left: 0; right: 0; z-index: 2;
    padding: 14px max(18px, env(safe-area-inset-right)) 14px max(18px, env(safe-area-inset-left));
    transition: opacity 0.25s var(--ease), transform 0.25s var(--ease);
  }
  .top {
    top: 0; display: flex; align-items: center; justify-content: space-between; gap: 16px;
    background: linear-gradient(rgba(0, 0, 0, 0.7), transparent);
  }
  .bottom {
    bottom: 0; padding-bottom: max(14px, env(safe-area-inset-bottom));
    background: linear-gradient(transparent, rgba(0, 0, 0, 0.8));
  }
  .idle .bar { opacity: 0; pointer-events: none; }
  .idle .top { transform: translateY(-6px); }
  .idle .bottom { transform: translateY(6px); }

  .title { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .people { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
  .chip {
    display: inline-flex; align-items: center; gap: 7px; height: 28px; padding: 0 11px;
    border-radius: 99px; background: rgba(255, 255, 255, 0.08); font-size: 13px;
  }
  .chip i { width: 7px; height: 7px; border-radius: 50%; background: var(--ok); }
  .chip i.warn { background: var(--warn); }

  .row { display: flex; align-items: center; gap: 4px; margin-top: 6px; }
  .icon {
    width: 40px; height: 40px; display: grid; place-items: center; border-radius: 8px;
    color: var(--text); opacity: 0.9; transition: background 0.15s var(--ease), opacity 0.15s;
  }
  .icon:hover { background: rgba(255, 255, 255, 0.08); opacity: 1; }
  .icon.on { color: var(--accent); background: rgba(255, 255, 255, 0.1); }
  .time { margin-left: 8px; font-size: 14px; white-space: nowrap; }
  .spacer { flex: 1; }

  .volume { display: flex; align-items: center; }
  .volume input { width: 0; opacity: 0; transition: width 0.2s var(--ease), opacity 0.2s; accent-color: var(--accent); }
  .volume:hover input, .volume:focus-within input { width: 84px; opacity: 1; margin-right: 6px; }

  .menu-wrap { position: relative; }
  .menu {
    position: absolute; right: 0; bottom: 48px; min-width: 220px; padding: 6px;
    background: rgba(18, 18, 18, 0.96); border: 1px solid var(--line); border-radius: var(--radius);
    backdrop-filter: blur(12px); box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
  }
  .menu button {
    width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 12px;
    padding: 9px 10px; border-radius: 6px; text-align: left; font-size: 14px;
  }
  .menu button:hover:not(:disabled) { background: rgba(255, 255, 255, 0.07); }
  .menu button:disabled { color: var(--faint); cursor: default; }
  .menu small { color: var(--faint); }

  .center { position: absolute; inset: 0; display: grid; place-items: center; pointer-events: none; z-index: 1; }
  .center > * { pointer-events: auto; }
  .status {
    display: flex; align-items: center; gap: 14px; padding: 14px 20px; border-radius: 99px;
    max-width: min(520px, calc(100vw - 32px)); text-align: center;
    background: rgba(0, 0, 0, 0.6); backdrop-filter: blur(8px);
  }
  .count { font-size: 40px; font-weight: 300; width: 40px; text-align: center; }
  .big {
    width: 76px; height: 76px; border-radius: 50%; display: grid; place-items: center; padding-left: 4px;
    background: rgba(0, 0, 0, 0.55); border: 1px solid rgba(255, 255, 255, 0.15); backdrop-filter: blur(8px);
    transition: transform 0.15s var(--ease), background 0.15s;
  }
  .big:hover { transform: scale(1.05); background: rgba(0, 0, 0, 0.7); }

  .toast {
    position: absolute; top: 64px; left: 50%; transform: translateX(-50%); z-index: 3;
    padding: 8px 16px; border-radius: 99px; background: rgba(18, 18, 18, 0.92);
    border: 1px solid var(--line); font-size: 14px; white-space: nowrap;
  }

  @media (max-width: 560px) {
    .volume, .row > .icon:nth-child(2), .row > .icon:nth-child(3) { display: none; }
    .chip { display: none; }
  }
</style>
