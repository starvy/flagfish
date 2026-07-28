// The player's own choice of view lives in localStorage, per browser: the admin picks what the
// instance leads with, and a player who wants the plain board back says so once. Absent means
// "no choice yet", which is not the same as having chosen the standard board — a player who never
// answered follows the instance when the admin changes it.
const STORAGE_KEY = "flagfish.portalView";

function storage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

function stored(): string | null {
  return storage()?.getItem(STORAGE_KEY) ?? null;
}

// One value for the whole tab, not one per consumer. The board resolves off this and so does the
// theme; two copies would mean a click that changed the board and left the palette behind.
let current: string | null = stored();
const listeners = new Set<() => void>();
let watching = false;

export function readPreference(): string | null {
  return current;
}

/** A browser that refuses storage still switches views — it just cannot remember across a reload. */
export function writePreference(value: string | null): void {
  const s = storage();
  if (s !== null) {
    if (value === null) s.removeItem(STORAGE_KEY);
    else s.setItem(STORAGE_KEY, value);
  }
  publish(value);
}

export function subscribePreference(listener: () => void): () => void {
  listeners.add(listener);
  watchOtherTabs();
  return () => void listeners.delete(listener);
}

function publish(value: string | null): void {
  if (current === value) return;
  current = value;
  for (const listener of listeners) listener();
}

// localStorage is shared between the tabs of an origin; the in-memory copy is not. Attached on the
// first subscriber and never removed — the store outlives every consumer of it.
function watchOtherTabs(): void {
  if (watching || typeof window === "undefined") return;
  watching = true;
  window.addEventListener("storage", (event: StorageEvent) => {
    // A null key is the whole store being cleared, which is ours too.
    if (event.key !== null && event.key !== STORAGE_KEY) return;
    publish(stored());
  });
}
