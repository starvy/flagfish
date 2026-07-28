import { useCallback, useSyncExternalStore } from "react";

/** One solve, as the stream spells it. */
export interface SolveEvent {
  solve_id: number;
  challenge_id: number;
  first_blood: boolean;
  /** RFC3339. */
  solved_at: string;
}

export type StreamStatus = "idle" | "connecting" | "open" | "closed";

export interface SolveStream {
  /** Newest first, capped. Live only — the server does not replay, and neither do we. */
  events: readonly SolveEvent[];
  status: StreamStatus;
}

const STREAM_URL = "/api/v1/events/solves";
const CAP = 100;
const BACKOFF_MIN = 1_000;
const BACKOFF_MAX = 30_000;

// One stream per session, not one per component, so a view with several subscribers still holds
// a single connection open.
let source: EventSource | null = null;
let refs = 0;
let backoff = BACKOFF_MIN;
let timer: ReturnType<typeof setTimeout> | null = null;

const seen = new Set<number>();
const listeners = new Set<() => void>();

let snapshot: SolveStream = { events: [], status: "idle" };

function emit(next: Partial<SolveStream>): void {
  snapshot = { ...snapshot, ...next };
  for (const l of listeners) l();
}

/**
 * Reads one frame. Every field is checked because this drives an animation keyed by challenge:
 * a frame missing its challenge would light up whatever country id `undefined` lands on.
 */
export function parseSolveEvent(raw: string): SolveEvent | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null; // a malformed frame is the server's bug; dropping it beats tearing the stream down
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const e = parsed as Partial<SolveEvent>;
  if (typeof e.solve_id !== "number" || !Number.isFinite(e.solve_id)) return null;
  if (typeof e.challenge_id !== "number" || !Number.isFinite(e.challenge_id)) return null;

  return {
    solve_id: e.solve_id,
    challenge_id: e.challenge_id,
    first_blood: e.first_blood === true,
    solved_at: typeof e.solved_at === "string" ? e.solved_at : "",
  };
}

function ingest(raw: string): void {
  const event = parseSolveEvent(raw);
  if (event === null) return;

  // There is no replay window, so a repeat can only come from a reconnect racing the server.
  // The id is the only thing that tells us we already drew it.
  if (seen.has(event.solve_id)) return;
  seen.add(event.solve_id);

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

  es.addEventListener("solve", (e: MessageEvent<string>) => ingest(e.data));

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

function getSnapshot(): SolveStream {
  return snapshot;
}

const IDLE: SolveStream = { events: [], status: "idle" };

/**
 * Subscribes to the live solve feed.
 *
 * Nothing here is persisted and nothing is scored off it: a solve arrives, a view draws
 * something for a second, and it is gone. The board's own queries remain the truth. `enabled`
 * exists because the stream is authenticated — pointing an EventSource at it while signed out
 * buys a reconnect loop against a 403.
 */
export function useSolveStream(options: { enabled?: boolean } = {}): SolveStream {
  const enabled = options.enabled ?? true;

  const sub = useCallback(
    (listener: () => void) => (enabled ? subscribe(listener) : () => {}),
    [enabled],
  );

  return useSyncExternalStore(
    sub,
    () => (enabled ? getSnapshot() : IDLE),
    () => IDLE,
  );
}

/** Drops the accumulated feed — for a sign-out, where the next session must not inherit it. */
export function resetSolveStream(): void {
  seen.clear();
  snapshot = { events: [], status: snapshot.status };
  for (const l of listeners) l();
}
