/**
 * The account's language preference, applied to the browser's own locale machinery.
 *
 * There is no message-catalog i18n yet, so this is not translation — it is the honest,
 * immediate consumer of the preference: `document.documentElement.lang` (which drives
 * hyphenation, spellcheck, and assistive tech) and the locale every `Intl` / `toLocale…`
 * date and number formatter resolves against. `undefined` means "follow the browser",
 * which is exactly what those APIs already do when passed `undefined`.
 */
let preferred: string | undefined;

export function applyLocalePreference(tag: string | null | undefined): void {
  preferred = tag ?? undefined;
  if (typeof document !== "undefined") {
    document.documentElement.lang = preferred ?? "";
  }
}

/** The preferred BCP 47 tag, or `undefined` to let the formatter fall back to the browser. */
export function preferredLocale(): string | undefined {
  return preferred;
}
