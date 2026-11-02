import type { PlaybackState } from './protocol';

/** Monotonic wall-clock in ms (immune to system clock jumps). */
export const localNow = (): number => performance.timeOrigin + performance.now();

/**
 * NTP-style offset estimator. Each ping/pong gives one sample; the sample with
 * the smallest round-trip in a sliding window is the least affected by
 * queueing delay, so its offset is used.
 */
export class Clock {
  private samples: { rtt: number; offset: number }[] = [];
  constructor(private readonly window = 10) {}

  /** c0: client send time, s: server time, c1: client receive time. */
  add(c0: number, s: number, c1: number): void {
    const rtt = c1 - c0;
    if (rtt < 0) return;
    this.samples.push({ rtt, offset: s - (c0 + c1) / 2 });
    if (this.samples.length > this.window) this.samples.shift();
  }

  get ready(): boolean {
    return this.samples.length >= 3;
  }

  private best() {
    let b = this.samples[0];
    for (const s of this.samples) if (b === undefined || s.rtt < b.rtt) b = s;
    return b;
  }

  /** Server minus client, ms. */
  get offset(): number {
    return this.best()?.offset ?? 0;
  }

  get rtt(): number {
    return this.best()?.rtt ?? 0;
  }

  serverNow(now = localNow()): number {
    return now + this.offset;
  }
}

/** Media position the room expects at server time `serverNow` (ms). */
export function targetPosition(s: PlaybackState, serverNow: number): number {
  if (s.paused || serverNow <= s.at) return s.position;
  return s.position + (serverNow - s.at) / 1000;
}

export type Correction =
  | { kind: 'none' }
  | { kind: 'rate'; rate: number }
  | { kind: 'seek'; to: number };

/** Below this, drift is imperceptible and left alone (seconds). */
export const DEADBAND = 0.04;
/** Above this, catching up by speed would take too long: seek instead. */
export const SEEK_THRESHOLD = 1.0;
/** Max playback-rate deviation (5% is inaudible for speech and music). */
export const MAX_RATE_DELTA = 0.05;

/**
 * Decide how to correct `drift` = actual − target (seconds, + means ahead).
 * Small drift is absorbed by nudging playbackRate so that it converges in
 * ~2 s without any visible jump; large drift is fixed with a seek.
 */
export function correct(drift: number, target: number): Correction {
  const a = Math.abs(drift);
  if (a < DEADBAND) return { kind: 'none' };
  if (a >= SEEK_THRESHOLD) return { kind: 'seek', to: target };
  const delta = Math.min(MAX_RATE_DELTA, a * 0.5);
  return { kind: 'rate', rate: drift > 0 ? 1 - delta : 1 + delta };
}

/** Seconds buffered contiguously ahead of `t`. */
export function bufferedAhead(buffered: TimeRanges, t: number): number {
  for (let i = 0; i < buffered.length; i++) {
    const start = buffered.start(i);
    const end = buffered.end(i);
    if (t >= start - 0.25 && t <= end) return end - t;
  }
  return 0;
}

/** Ms of continuous 'waiting' while playing before a viewer reports a stall. */
export const STALL_GRACE_MS = 700;

export interface PlayerSnapshot {
  /** HTMLMediaElement.readyState */
  readyState: number;
  seeking: boolean;
  /** The element can't play this source at all: never hold the room for it. */
  failed: boolean;
  /** The room expects us to be playing right now. */
  shouldPlay: boolean;
  /** Ms since the element fired 'waiting' without recovering (0 = not waiting). */
  stalledMs: number;
  /** Within the last half second of the media (no "future data" exists). */
  atEnd: boolean;
}

/**
 * Whether this viewer should hold the room (stall barrier). While paused, trust
 * the browser's HAVE_FUTURE_DATA: a paused element stops reading ahead after a
 * small byte budget (~2 MB in Windows Chrome), so "N seconds buffered" deadlocks.
 */
export function isBuffering(p: PlayerSnapshot): boolean {
  if (p.failed) return false;
  if (p.shouldPlay) return p.stalledMs > STALL_GRACE_MS;
  if (p.atEnd) return false;
  return p.seeking || p.readyState < 3;
}
