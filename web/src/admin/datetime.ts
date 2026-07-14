// The clock crosses two representations: the API speaks RFC 3339 in UTC, and
// <input type="datetime-local"> speaks wall-clock in the operator's zone with no offset at all.
// Every conversion between them lives here, so a screen never hand-rolls one.

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/** RFC 3339 -> the `YYYY-MM-DDTHH:mm` a datetime-local input accepts, in local time. */
export function isoToLocalInput(iso: string | null | undefined): string {
  if (!iso) return "";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "";
  return (
    `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}` +
    `T${pad(at.getHours())}:${pad(at.getMinutes())}`
  );
}

/** The input's wall-clock back to RFC 3339 UTC. `undefined` — never null — when it will not parse. */
export function localInputToIso(local: string): string | undefined {
  if (local.trim() === "") return undefined;
  const at = new Date(local);
  if (Number.isNaN(at.getTime())) return undefined;
  return at.toISOString();
}

/** The operator's zone, spelled out: a clock without one is a support ticket. */
export function localZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone;
}
