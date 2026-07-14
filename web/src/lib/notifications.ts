import { useCallback, useSyncExternalStore } from "react";

/** One notification, exactly as the stream and `GET /notifications` both spell it. */
export interface NotificationEvent {
  id: number;
  title: string;
  content: string;
  /** RFC3339. */
  date: string;
}

export type StreamStatus = "idle" | "connecting" | "open" | "closed";

export interface NotificationStream {
  /** Newest first. Deduped on id and capped. */
  events: readonly NotificationEvent[];
  status: StreamStatus;
}

const STREAM_URL = "/api/v1/notifications/stream";
const CAP = 200;
const BACKOFF_MIN = 1_000;
const BACKOFF_MAX = 30_000;

// One stream per session, not one per component: the server replays up to 50 events on every
// connect, so a second EventSource would cost a second replay and buy nothing.
let source: EventSource | null = null;
let refs = 0;
let backoff = BACKOFF_MIN;
let timer: ReturnType<typeof setTimeout> | null = null;

const seen = new Set<number>();
const listeners = new Set<() => void>();

let snapshot: NotificationStream = { events: [], status: "idle" };

function emit(next: Partial<NotificationStream>): void {
  snapshot = { ...snapshot, ...next };
  for (const l of listeners) l();
}

function ingest(raw: string): void {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return; // a malformed frame is the server's bug; dropping it beats tearing the stream down
  }
  if (typeof parsed !== "object" || parsed === null) return;
  const n = parsed as Partial<NotificationEvent>;
  if (typeof n.id !== "number") return;

  // The replay window deliberately overlaps the live feed by up to one event; id is the
  // only thing that tells us we already have it.
  if (seen.has(n.id)) return;
  seen.add(n.id);

  const event: NotificationEvent = {
    id: n.id,
    title: n.title ?? "",
    content: n.content ?? "",
    date: n.date ?? "",
  };
  emit({ events: [event, ...snapshot.events].slice(0, CAP) });
}

function connect(): void {
  if (source !== null || refs === 0) return;

  emit({ status: "connecting" });
  const es = new EventSource(STREAM_URL, { withCredentials: true });
  source = es;

  es.onopen = () => {
    backoff = BACKOFF_MIN;
    emit({ status: "open" });
  };

  es.addEventListener("notification", (e: MessageEvent<string>) => ingest(e.data));

  es.onerror = () => {
    // A dropped connection leaves the EventSource reconnecting on its own; a rejected one
    // (403 before the CTF opens, say) closes it for good and is ours to retry.
    if (es.readyState !== EventSource.CLOSED) {
      emit({ status: "connecting" });
      return;
    }
    es.close();
    if (source === es) source = null;
    emit({ status: "closed" });
    if (refs > 0) schedule();
  };
}

function schedule(): void {
  if (timer !== null) return;
  const wait = backoff;
  backoff = Math.min(backoff * 2, BACKOFF_MAX);
  timer = setTimeout(() => {
    timer = null;
    connect();
  }, wait);
}

function disconnect(): void {
  if (timer !== null) {
    clearTimeout(timer);
    timer = null;
  }
  source?.close();
  source = null;
  backoff = BACKOFF_MIN;
  emit({ status: "idle" });
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  refs += 1;
  connect();
  return () => {
    listeners.delete(listener);
    refs -= 1;
    if (refs === 0) disconnect();
  };
}

function getSnapshot(): NotificationStream {
  return snapshot;
}

const IDLE: NotificationStream = { events: [], status: "idle" };

/**
 * Subscribes to the live notification feed.
 *
 * Callers get the events and render them however they like — the drawer and the toasts are
 * not this hook's business. `enabled` exists because the stream is authenticated: pointing an
 * EventSource at it while signed out just buys a reconnect loop against a 403.
 */
export function useNotificationStream(options: { enabled?: boolean } = {}): NotificationStream {
  const enabled = options.enabled ?? true;

  const sub = useCallback(
    (listener: () => void) => (enabled ? subscribe(listener) : () => {}),
    [enabled],
  );

  const snap = useSyncExternalStore(
    sub,
    () => (enabled ? getSnapshot() : IDLE),
    () => IDLE,
  );

  return snap;
}

/** Drops the accumulated feed — for a sign-out, where the next session must not inherit it. */
export function resetNotificationStream(): void {
  seen.clear();
  snapshot = { events: [], status: snapshot.status };
  for (const l of listeners) l();
}
