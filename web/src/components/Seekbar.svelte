<script lang="ts">
  import { formatTime } from '../lib/format';

  let { duration, current, buffered, onseek }: {
    duration: number; current: number; buffered: number; onseek: (t: number) => void;
  } = $props();

  let track: HTMLDivElement;
  let hover = $state<number | null>(null); // 0..1
  let dragging = $state<number | null>(null);

  const pct = (t: number) => (duration > 0 ? Math.min(100, (t / duration) * 100) : 0);
  const played = $derived(dragging !== null ? dragging * 100 : pct(current));

  function ratio(e: PointerEvent) {
    const r = track.getBoundingClientRect();
    return Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
  }
  function down(e: PointerEvent) {
    if (!duration) return;
    track.setPointerCapture(e.pointerId);
    dragging = ratio(e);
  }
  function move(e: PointerEvent) {
    hover = ratio(e);
    if (dragging !== null) dragging = hover;
  }
  function up() {
    if (dragging !== null) onseek(dragging * duration);
    dragging = null;
  }
  function key(e: KeyboardEvent) {
    const step = e.shiftKey ? 60 : 10;
    if (e.key === 'ArrowLeft') { e.stopPropagation(); e.preventDefault(); onseek(current - step); }
    if (e.key === 'ArrowRight') { e.stopPropagation(); e.preventDefault(); onseek(current + step); }
  }
</script>

<div class="seek" bind:this={track} role="slider" tabindex="0" aria-label="Seek"
  aria-valuemin={0} aria-valuemax={Math.round(duration)} aria-valuenow={Math.round(current)}
  aria-valuetext={formatTime(current)}
  onpointerdown={down} onpointermove={move} onpointerup={up} onpointercancel={() => (dragging = null)}
  onpointerleave={() => (hover = null)} onkeydown={key}>
  <div class="rail">
    <div class="buf" style:width="{pct(buffered)}%"></div>
    <div class="fill" style:width="{played}%"></div>
  </div>
  <div class="knob" class:show={dragging !== null || hover !== null} style:left="{played}%"></div>
  {#if hover !== null && duration > 0}
    <div class="tip" style:left="{hover * 100}%">{formatTime(hover * duration)}</div>
  {/if}
</div>

<style>
  .seek { position: relative; height: 22px; display: flex; align-items: center; cursor: pointer; touch-action: none; }
  .rail { position: relative; width: 100%; height: 3px; border-radius: 3px; background: rgba(255, 255, 255, 0.18); overflow: hidden; transition: height 0.15s var(--ease); }
  .seek:hover .rail, .seek:focus-visible .rail { height: 5px; }
  .buf, .fill { position: absolute; inset: 0 auto 0 0; }
  .buf { background: rgba(255, 255, 255, 0.3); }
  .fill { background: var(--accent); }
  .knob {
    position: absolute; top: 50%; width: 13px; height: 13px; margin: -6.5px 0 0 -6.5px; border-radius: 50%;
    background: var(--accent); transform: scale(0); transition: transform 0.15s var(--ease);
  }
  .knob.show, .seek:focus-visible .knob { transform: scale(1); }
  .tip {
    position: absolute; bottom: 24px; transform: translateX(-50%); padding: 3px 8px; border-radius: 6px;
    background: rgba(18, 18, 18, 0.95); border: 1px solid var(--line); font-size: 12px; pointer-events: none;
  }
</style>
