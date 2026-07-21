// Pure helpers behind the manual-award panel: the running score contribution and the two rules the
// grant form echoes off the server. Kept separate from the component so the arithmetic is testable
// in isolation.

export interface AwardValue {
  value: number;
}

// The net points these adjustments currently add to the account's score. A revoke is a real
// deletion, so a revoked row simply isn't in the list and never counts here.
export function netContribution(awards: readonly AwardValue[]): number {
  return awards.reduce((sum, a) => sum + a.value, 0);
}

export function formatSignedPoints(value: number): string {
  return value > 0 ? `+${value}` : String(value);
}

// The signed whole number the grant form will submit, or null when the box is empty, fractional, a
// non-number, or a no-op zero — the same shape the server rejects.
export function parsePoints(raw: string): number | null {
  const n = Number(raw);
  return Number.isInteger(n) && n !== 0 ? n : null;
}

// A nonzero integer and a non-blank reason: the button reads as disabled rather than the submit
// bouncing a 422 back off the server's minLength/int32 validation.
export function isGrantValid(rawValue: string, reason: string): boolean {
  return parsePoints(rawValue) !== null && reason.trim() !== "";
}
