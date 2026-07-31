import raw from "./data/countries.geo.json?raw";
import type { CountryFeature } from "./geography";

// Imported as text and parsed, not imported as a module. The outlines are ~190 kB of nested
// arrays: as a JavaScript literal the engine would compile every one of them, where JSON.parse
// reads the same bytes in one pass. It also keeps the file a plain .json the generator can write
// and a human can diff.
const collection = JSON.parse(raw) as { features: CountryFeature[] };

/** Every country the globe draws. */
export const COUNTRY_FEATURES: readonly CountryFeature[] = collection.features;

export function codeOfFeature(feature: object): string {
  return (feature as CountryFeature).properties.code;
}
