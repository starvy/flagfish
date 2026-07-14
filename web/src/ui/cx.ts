export type ClassValue = string | false | null | undefined;

// Join class names, dropping the falsy ones. The whole of what `clsx` gives us for
// the way this kit uses it — conditional modifier classes — without the dependency.
export function cx(...parts: ClassValue[]): string {
  return parts.filter(Boolean).join(" ");
}
