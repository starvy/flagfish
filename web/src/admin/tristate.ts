/**
 * The three-state PATCH field.
 *
 * The admin surface distinguishes three writes on the same key: omitting it keeps whatever the
 * server has, `null` clears it, and a value sets it. Collapsing that onto a nullable input is
 * the bug this type exists to prevent — an empty box would then silently clear a field the
 * operator never touched. The control is therefore an explicit choice, and "keep" is the only
 * default a form may start in.
 */
export type TriMode = "keep" | "clear" | "set";

export interface TriValue {
  mode: TriMode;
  /** The draft the `set` input holds. Carried across mode switches so toggling is not destructive. */
  value: string;
}

/** A field starts in `keep`, pre-loaded with what the server has, so `set` opens on the truth. */
export function triKeep(current = ""): TriValue {
  return { mode: "keep", value: current };
}

/**
 * Fold a tri-state field into what the request body wants: `undefined` omits the key (and
 * `JSON.stringify` drops it on the wire), `null` clears it, anything else sets it.
 *
 * `encode` returns `undefined` for input it cannot parse. That is a validation failure, not a
 * clear — the caller must catch it before submitting rather than send a `null` the operator
 * never asked for.
 */
export function triPatch<T>(
  field: TriValue,
  encode: (raw: string) => T | undefined,
): T | null | undefined {
  switch (field.mode) {
    case "keep":
      return undefined;
    case "clear":
      return null;
    case "set":
      return encode(field.value);
  }
}

/** True when `set` was chosen but the value does not encode — the one state a form must refuse. */
export function triInvalid<T>(field: TriValue, encode: (raw: string) => T | undefined): boolean {
  return field.mode === "set" && encode(field.value) === undefined;
}

/** Identity encode for a plain text field: an empty box in `set` mode is not a value. */
export function encodeText(raw: string): string | undefined {
  const trimmed = raw.trim();
  return trimmed === "" ? undefined : trimmed;
}

export function encodeInt(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (trimmed === "") return undefined;
  const n = Number(trimmed);
  return Number.isInteger(n) ? n : undefined;
}
