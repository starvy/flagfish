import { COUNTRY_ANCHORS } from "./data/anchors";
import { COUNTRY_NAMES } from "./data/countryNames";

export interface LatLng {
  lat: number;
  lng: number;
}

/** One drawn country: the slim shape `countries.geo.json` carries. */
export interface CountryFeature {
  type: "Feature";
  properties: { code: string };
  geometry: {
    type: "Polygon" | "MultiPolygon";
    coordinates: number[][][] | number[][][][];
  };
}

/**
 * Where the pin for a country goes.
 *
 * Null for a code the map does not draw — the 1:110m cut leaves out microstates, and a code an
 * operator typed by hand may be no country at all. A caller that cannot place a challenge lists
 * it instead; it never guesses a position.
 */
export function anchorOf(code: string): LatLng | null {
  return COUNTRY_ANCHORS[code] ?? null;
}

/** The English name, or the code itself — never an empty label. */
export function nameOf(code: string): string {
  return COUNTRY_NAMES[code] ?? code;
}

export function isDrawn(code: string): boolean {
  return COUNTRY_ANCHORS[code] !== undefined;
}
