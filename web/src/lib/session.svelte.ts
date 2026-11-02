import type { ClientMsg, MediaItem, PlaybackState, ServerMsg, Viewer } from './protocol';
import { Clock, localNow } from './sync';

/**
 * Reactive connection to the room: a reconnecting WebSocket plus the clock
 * estimator. Components read its fields; the player issues commands via cmd().
 */
export class Session {
  connected = $state(false);
  you = $state('');
  name = $state('');
  host = $state(false);
  state = $state<PlaybackState>({ seq: 0, paused: true, position: 0, at: 0 });
  viewers = $state<Viewer[]>([]);
  media = $state<MediaItem | null>(null);
  /** Increments whenever a new authoritative state arrives. */
  stateVersion = $state(0);

  readonly clock = new Clock();
  private ws: WebSocket | null = null;
  private retry = 0;
  private pingTimer: ReturnType<typeof setTimeout> | undefined;
  private closed = false;

  connect(): void {
    this.closed = false;
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${proto}//${location.host}/ws`);
    this.ws = ws;
    ws.onopen = () => {
      this.retry = 0;
      this.connected = true;
      this.schedulePings(8, 120); // fast burst for an accurate initial offset
    };
    ws.onmessage = (ev) => this.onMessage(JSON.parse(ev.data as string) as ServerMsg);
    ws.onclose = () => {
      this.connected = false;
      clearTimeout(this.pingTimer);
      if (this.closed) return;
      const delay = Math.min(5000, 250 * 2 ** this.retry++);
      setTimeout(() => this.connect(), delay);
    };
  }

  close(): void {
    this.closed = true;
    this.ws?.close();
  }

  send(msg: ClientMsg): void {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(msg));
  }

  cmd(op: 'play' | 'pause' | 'seek', pos?: number): void {
    this.send({ t: 'cmd', op, pos });
  }

  private schedulePings(burst: number, gap: number): void {
    clearTimeout(this.pingTimer);
    const tick = (left: number) => {
      this.send({ t: 'ping', c: localNow() });
      this.pingTimer = setTimeout(() => tick(left - 1), left > 1 ? gap : 5000);
    };
    tick(burst);
  }

  private onMessage(m: ServerMsg): void {
    switch (m.t) {
      case 'pong':
        this.clock.add(m.c, m.s, localNow());
        break;
      case 'hello':
        this.you = m.you;
        this.name = m.name;
        this.host = m.host;
        this.viewers = m.viewers ?? [];
        this.media = normalize(m.media);
        this.setState(m.state);
        break;
      case 'state':
        this.setState(m.state);
        break;
      case 'viewers':
        this.viewers = m.viewers ?? [];
        break;
      case 'media':
        this.media = normalize(m.media);
        break;
    }
  }

  private setState(s: PlaybackState): void {
    this.state = s;
    this.stateVersion++;
  }
}

/** Defensive: never let a missing list crash rendering. */
function normalize(m: MediaItem | null): MediaItem | null {
  return m && { ...m, variants: m.variants ?? [], subs: m.subs ?? [], imageSubs: m.imageSubs ?? 0 };
}
