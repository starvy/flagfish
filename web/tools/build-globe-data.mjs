// Regenerates everything under src/globe/data/. Run from web/:
//
//   node tools/build-globe-data.mjs
//
// Both inputs are already on disk after `npm install`, so this needs no network. See
// src/globe/data/PROVENANCE.md for what they are and why they are checked in.

import { createRequire } from "node:module";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const outDir = join(here, "..", "src", "globe", "data");

// Reached by path rather than by import: the package's `exports` map covers neither its example
// data nor its manifest. This is the same release-pinned copy the upstream examples fetch from a
// CDN, which is exactly what we are here to stop doing.
const THREE_GLOBE = join(here, "..", "node_modules", "three-globe");
const SOURCE = join(THREE_GLOBE, "example", "country-polygons", "ne_110m_admin_0_countries.geojson");
const THREE_GLOBE_VERSION = JSON.parse(
  readFileSync(join(THREE_GLOBE, "package.json"), "utf8"),
).version;

// Coordinate precision. The source is the 1:110m ("small scale") cut, whose vertices sit around a
// kilometre apart; three decimals is ~110 m, well under what a vertex can resolve, and it halves
// the file.
const PRECISION = 3;

/**
 * Natural Earth writes -99 into ISO_A2 for territories whose code is disputed or which it treats
 * as part of a parent. These are the ones the 110m cut draws as separate features and which do
 * have a code a player would expect to pick. Keyed by ADM0_A3, which is stable across releases.
 */
const ISO_A2_FIXUPS = {
  FRA: "FR", // France: -99 because the feature is metropolitan France plus the overseas departments
  NOR: "NO", // Norway: -99 because the feature includes Svalbard and Jan Mayen
  KOS: "XK", // Kosovo: no ISO 3166-1 code; XK is the user-assigned one everyone uses
};

// Features with no code anyone can pick. Drawn as neutral land, never as a country.
const UNCODED = new Set(["Somaliland", "N. Cyprus"]);

/**
 * CLDR names more two-letter regions than ISO 3166-1 assigns. Withdrawn codes are dropped by
 * canonicalisation below — `und-UK` canonicalises to GB, so UK is not its own region. These are
 * the rest: groupings, pseudo-locales, and codes ISO holds in reserve rather than assigns.
 */
const NOT_A_COUNTRY = new Set([
  "EU", // European Union
  "EZ", // Eurozone
  "UN", // United Nations
  "QO", // Outlying Oceania
  "XA", // pseudo-locale
  "XB", // pseudo-locale
  "ZZ", // unknown region
  "AC", // Ascension Island — exceptionally reserved
  "CP", // Clipperton Island — exceptionally reserved
  "CQ", // Sark — exceptionally reserved
  "DG", // Diego Garcia — exceptionally reserved
  "EA", // Ceuta & Melilla — exceptionally reserved
  "IC", // Canary Islands — exceptionally reserved
  "TA", // Tristan da Cunha — exceptionally reserved
]);

// Kosovo has no ISO 3166-1 code. XK is the user-assigned one in universal practice, it is what
// CLDR names, and the map draws the country — so an operator has to be able to pick it.
const USER_ASSIGNED = new Set(["XK"]);

const round = (n) => Number(n.toFixed(PRECISION));

function roundRing(ring) {
  const out = [];
  for (const [lng, lat] of ring) {
    const point = [round(lng), round(lat)];
    // Rounding can collapse neighbouring vertices onto each other; a repeated point is noise in
    // the file and a degenerate segment in the triangulator.
    const last = out[out.length - 1];
    if (last && last[0] === point[0] && last[1] === point[1]) continue;
    out.push(point);
  }
  // A ring must stay closed after all that.
  const first = out[0];
  const last = out[out.length - 1];
  if (first && last && (first[0] !== last[0] || first[1] !== last[1])) out.push([...first]);
  return out;
}

function roundGeometry(geometry) {
  if (geometry.type === "Polygon") {
    return { type: "Polygon", coordinates: geometry.coordinates.map(roundRing) };
  }
  if (geometry.type === "MultiPolygon") {
    return {
      type: "MultiPolygon",
      coordinates: geometry.coordinates.map((polygon) => polygon.map(roundRing)),
    };
  }
  throw new Error(`unexpected geometry ${geometry.type}`);
}

/** Signed area of a ring in degrees², by the shoelace formula. Sign is orientation; we want size. */
function ringArea(ring) {
  let sum = 0;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    sum += (ring[j][0] + ring[i][0]) * (ring[j][1] - ring[i][1]);
  }
  return sum / 2;
}

/** Area-weighted centroid of a ring, in lon/lat. */
function ringCentroid(ring) {
  let x = 0;
  let y = 0;
  let area = 0;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    const cross = ring[j][0] * ring[i][1] - ring[i][0] * ring[j][1];
    x += (ring[j][0] + ring[i][0]) * cross;
    y += (ring[j][1] + ring[i][1]) * cross;
    area += cross;
  }
  area /= 2;
  if (area === 0) return null;
  return [x / (6 * area), y / (6 * area)];
}

/**
 * A pin goes on the country's largest landmass, not on the centre of its bounding box: the mean
 * of France and French Guiana is the Atlantic, and a pin in the sea reads as a bug.
 */
function anchorOf(geometry) {
  const polygons = geometry.type === "Polygon" ? [geometry.coordinates] : geometry.coordinates;
  let best = null;
  let bestArea = -1;
  for (const polygon of polygons) {
    const outer = polygon[0];
    const area = Math.abs(ringArea(outer));
    if (area > bestArea) {
      bestArea = area;
      best = outer;
    }
  }
  const centre = best === null ? null : ringCentroid(best);
  if (centre === null) return null;
  return [round(centre[1]), round(centre[0])]; // lat, lng
}

function codeOf(properties) {
  const iso = properties.ISO_A2;
  if (typeof iso === "string" && /^[A-Z]{2}$/.test(iso)) return iso;
  return ISO_A2_FIXUPS[properties.ADM0_A3] ?? null;
}

function isoCountryNames() {
  const display = new Intl.DisplayNames(["en"], { type: "region", fallback: "none" });
  const names = {};
  for (let a = 65; a <= 90; a++) {
    for (let b = 65; b <= 90; b++) {
      const code = String.fromCharCode(a, b);
      if (NOT_A_COUNTRY.has(code)) continue;
      // A withdrawn code canonicalises to whatever replaced it — `und-ZR` is CD. Keeping both
      // would offer the same country twice under two names.
      if (!USER_ASSIGNED.has(code) && new Intl.Locale(`und-${code}`).region !== code) continue;
      const name = display.of(code);
      // fallback: "none" returns undefined for a code CLDR does not assign.
      if (name === undefined || name === code) continue;
      names[code] = name;
    }
  }
  return names;
}

const source = JSON.parse(readFileSync(SOURCE, "utf8"));

const features = [];
const anchors = {};
const skipped = [];

for (const feature of source.features) {
  const code = codeOf(feature.properties);
  if (code === null) {
    if (!UNCODED.has(feature.properties.NAME)) skipped.push(feature.properties.NAME);
    continue;
  }
  if (anchors[code] !== undefined) throw new Error(`two features claim ${code}`);

  const geometry = roundGeometry(feature.geometry);
  const anchor = anchorOf(geometry);
  if (anchor === null) throw new Error(`${code} has no usable centroid`);

  features.push({ type: "Feature", properties: { code }, geometry });
  anchors[code] = anchor;
}

if (skipped.length > 0) {
  throw new Error(`unhandled features without an ISO 3166-1 alpha-2 code: ${skipped.join(", ")}`);
}

const names = isoCountryNames();
const missing = Object.keys(anchors).filter((code) => names[code] === undefined);
if (missing.length > 0) throw new Error(`drawn but unnamed: ${missing.join(", ")}`);

mkdirSync(outDir, { recursive: true });

writeFileSync(
  join(outDir, "countries.geo.json"),
  `${JSON.stringify({ type: "FeatureCollection", features })}\n`,
);

const banner = (what) =>
  `// Generated by tools/build-globe-data.mjs — do not edit by hand.\n` +
  `// ${what}\n` +
  `// Source: Natural Earth 1:110m Admin 0 – Countries (public domain), as bundled in\n` +
  `// three-globe@${THREE_GLOBE_VERSION}. See PROVENANCE.md.\n`;

const entries = (obj, format) =>
  Object.keys(obj)
    .sort()
    .map((code) => `  ${code}: ${format(obj[code])},`)
    .join("\n");

writeFileSync(
  join(outDir, "anchors.ts"),
  `${banner("Where a country's pin goes: the area-weighted centroid of its largest landmass.")}
import type { LatLng } from "../geography";

export const COUNTRY_ANCHORS: Readonly<Record<string, LatLng>> = {
${entries(anchors, ([lat, lng]) => `{ lat: ${lat}, lng: ${lng} }`)}
};
`,
);

writeFileSync(
  join(outDir, "countryNames.ts"),
  `// Generated by tools/build-globe-data.mjs — do not edit by hand.
// Every ISO 3166-1 alpha-2 code CLDR assigns a region name to, in English.
// Source: the Unicode CLDR data shipped with Node's ICU, read through Intl.DisplayNames.
// See PROVENANCE.md.

/** Code → English name. The admin country picker searches this; the globe labels from it. */
export const COUNTRY_NAMES: Readonly<Record<string, string>> = {
${entries(names, (name) => JSON.stringify(name))}
};

/** Sorted by name, for a picker. */
export const COUNTRY_LIST: ReadonlyArray<{ code: string; name: string }> = Object.entries(
  COUNTRY_NAMES,
)
  .map(([code, name]) => ({ code, name }))
  .sort((a, b) => a.name.localeCompare(b.name));
`,
);

console.log(
  `countries drawn: ${features.length}\n` +
    `iso codes named: ${Object.keys(names).length}\n` +
    `three-globe:     ${THREE_GLOBE_VERSION}`,
);
