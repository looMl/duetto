// Mirrors internal/room and internal/media (Go). Keep in sync.

export interface PlaybackState {
  seq: number;
  paused: boolean;
  position: number; // seconds
  at: number; // server unix ms
  waiting?: string[];
  by?: string;
}

export interface Viewer {
  id: string;
  name: string;
  host: boolean;
  buffering: boolean;
  ahead: number;
  drift: number;
  unsupported: boolean;
}

export interface Variant {
  id: string;
  label: string;
  codecs: string;
  width: number;
  height: number;
  hdr: boolean;
  bitrate: number;
  lossless: boolean;
}

export interface Subtitle {
  id: string;
  lang: string;
  label: string;
}

export interface MediaItem {
  id: string;
  title: string;
  duration: number;
  variants: Variant[];
  subs: Subtitle[];
  /** Embedded picture-based subtitle tracks that browsers can't show. */
  imageSubs: number;
}

export type ServerMsg =
  | {
      t: 'hello';
      you: string;
      name: string;
      host: boolean;
      state: PlaybackState;
      viewers: Viewer[];
      media: MediaItem | null;
    }
  | { t: 'pong'; c: number; s: number }
  | { t: 'state'; state: PlaybackState }
  | { t: 'viewers'; viewers: Viewer[] }
  | { t: 'media'; media: MediaItem | null };

export type ClientMsg =
  | { t: 'ping'; c: number }
  | { t: 'cmd'; op: 'play' | 'pause' | 'seek'; pos?: number }
  | { t: 'status'; buffering: boolean; ahead: number; drift: number; unsupported: boolean };
