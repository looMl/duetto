import { describe, expect, it } from 'vitest';
import { Clock, correct, targetPosition, bufferedAhead, isBuffering } from './sync';

describe('Clock', () => {
  it('uses the minimum-RTT sample', () => {
    const c = new Clock();
    // True offset is +1000 ms. Asymmetric queueing pollutes slow samples.
    c.add(0, 1000 + 300, 400); // rtt 400, offset 1100
    c.add(1000, 2000 + 10, 1020); // rtt 20, offset 1000
    c.add(2000, 3000 + 150, 2200); // rtt 200, offset 1050
    expect(c.rtt).toBe(20);
    expect(c.offset).toBe(1000);
    expect(c.ready).toBe(true);
    expect(c.serverNow(5000)).toBe(6000);
  });

  it('ignores negative RTT and slides its window', () => {
    const c = new Clock(2);
    c.add(10, 0, 5);
    expect(c.ready).toBe(false);
    c.add(0, 100, 10); // offset 95
    c.add(0, 200, 20); // offset 190
    c.add(0, 300, 30); // offset 285, evicts first
    expect(c.offset).toBe(190);
  });
});

describe('targetPosition', () => {
  const base = { seq: 1, position: 10, at: 1000 };
  it('is frozen when paused', () => {
    expect(targetPosition({ ...base, paused: true }, 9999)).toBe(10);
  });
  it('waits for a scheduled start', () => {
    expect(targetPosition({ ...base, paused: false }, 900)).toBe(10);
  });
  it('advances in real time when playing', () => {
    expect(targetPosition({ ...base, paused: false }, 3500)).toBe(12.5);
  });
});

describe('correct', () => {
  it('leaves imperceptible drift alone', () => {
    expect(correct(0.03, 5)).toEqual({ kind: 'none' });
  });
  it('slows down when ahead and speeds up when behind, capped at 5%', () => {
    expect(correct(0.06, 5)).toEqual({ kind: 'rate', rate: 0.97 });
    expect(correct(-0.06, 5)).toEqual({ kind: 'rate', rate: 1.03 });
    expect(correct(0.5, 5)).toEqual({ kind: 'rate', rate: 0.95 });
  });
  it('seeks on large drift', () => {
    expect(correct(-2, 42)).toEqual({ kind: 'seek', to: 42 });
  });
});

describe('bufferedAhead', () => {
  const ranges = (r: [number, number][]): TimeRanges => ({
    length: r.length,
    start: (i: number) => r[i]![0],
    end: (i: number) => r[i]![1],
  });
  it('measures the range containing t', () => {
    expect(bufferedAhead(ranges([[0, 5], [10, 30]]), 12)).toBe(18);
    expect(bufferedAhead(ranges([[0, 5]]), 7)).toBe(0);
  });
});

describe('isBuffering', () => {
  const base = { readyState: 4, seeking: false, failed: false, shouldPlay: false, stalledMs: 0, atEnd: false };

  it('regression: paused with a short browser read-ahead is ready', () => {
    // Observed on Windows Chrome: readyState 4, networkState IDLE, 2.325 s buffered.
    expect(isBuffering(base)).toBe(false);
  });
  it('paused without future data, or mid-seek, is not ready', () => {
    expect(isBuffering({ ...base, readyState: 2 })).toBe(true);
    expect(isBuffering({ ...base, readyState: 0 })).toBe(true);
    expect(isBuffering({ ...base, seeking: true })).toBe(true);
  });
  it('playing: only a sustained stall counts', () => {
    expect(isBuffering({ ...base, shouldPlay: true, stalledMs: 300 })).toBe(false);
    expect(isBuffering({ ...base, shouldPlay: true, stalledMs: 900 })).toBe(true);
  });
  it('never blocks the room for a failed source or at the end', () => {
    expect(isBuffering({ ...base, readyState: 0, failed: true })).toBe(false);
    expect(isBuffering({ ...base, readyState: 2, atEnd: true })).toBe(false);
  });
});
