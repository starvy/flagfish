// The user's saved theme choice lives in localStorage so it survives reloads and is
// readable synchronously before first paint. Absent means "no choice yet"; the
// resolver then falls back to the instance default and the system preference.
const STORAGE_KEY = "flagfish.theme";

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

export function prefersDark(): boolean {
  return typeof matchMedia !== "undefined" && matchMedia("(prefers-color-scheme: dark)").matches;
}
