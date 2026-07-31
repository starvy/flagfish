# Globe data

Everything in this directory is generated and checked in. Nothing here is fetched at
runtime: a CTF is routinely run on an isolated network, the app is served under a strict
CSP with no external origins, and a world map that fails to load is a board a player cannot
reach. The whole globe ships in the binary with the rest of the SPA.

Regenerate with, from `web/`:

    node tools/build-globe-data.mjs

Both inputs are already on disk after `npm install`, so the generator needs no network. It
fails loudly rather than emitting a partial file — an uncoded country, a duplicate code or a
drawn country with no name all stop it.

## `countries.geo.json` — country outlines

**Natural Earth 1:110m Admin 0 – Countries**, public domain (Natural Earth places all its
map data in the public domain and asks for no permission or credit; the courtesy credit is
"Made with Natural Earth"). Taken from the copy bundled at
`node_modules/three-globe/example/country-polygons/ne_110m_admin_0_countries.geojson`
in **three-globe 2.45.2**, which arrives as a dependency of `react-globe.gl`. That is the
same release-pinned file the upstream examples load from a CDN.

The generator rewrites it rather than copying it:

- every property but the country code is dropped — the source carries some 60 fields per
  feature, of which we render one;
- coordinates are rounded to 3 decimals (~110 m, well under what a 1:110m vertex resolves),
  and vertices that collapse onto each other are removed;
- features Natural Earth leaves as `ISO_A2: "-99"` are given their code by an explicit,
  commented table in the generator (France, Norway, Kosovo). Somaliland and Northern
  Cyprus have no code anyone can pick and are dropped; they are not drawn.

488 kB → 189 kB, 175 countries.

## `anchors.ts` — where a pin goes

Derived from the same outlines: the area-weighted centroid of each country's **largest**
landmass. The largest, not the mean of all of them — the average of France and French
Guiana is open ocean, and a pin in the sea reads as a bug.

## `countryNames.ts` — code → English name

Generated from the **Unicode CLDR** data shipped inside Node's ICU, read through
`Intl.DisplayNames`. CLDR is distributed under the Unicode License (permissive, and this is
data extracted from it rather than the data itself).

CLDR names more two-letter regions than ISO 3166-1 assigns, so the generator filters:
withdrawn codes are dropped by canonicalisation (`und-UK` canonicalises to `GB`, so `UK` is
not a region of its own), and groupings, pseudo-locales and exceptionally reserved codes are
dropped by an explicit list. Kosovo is kept under the user-assigned `XK`: it has no ISO
code, it is what CLDR names, and the map draws it.

The result is 250 codes — the 249 ISO 3166-1 alpha-2 assignments plus `XK`. A country the
map does not draw is still pickable; the challenge simply lists as unplaced instead of
getting a pin.
