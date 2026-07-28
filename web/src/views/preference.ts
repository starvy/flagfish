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

export function readPreference(): string | null {
  return storage()?.getItem(STORAGE_KEY) ?? null;
}

export function writePreference(value: string | null): void {
  const s = storage();
  if (!s) return;
  if (value === null) s.removeItem(STORAGE_KEY);
  else s.setItem(STORAGE_KEY, value);
}
