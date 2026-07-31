// Gzips the fingerprinted assets after `vite build` and deletes the raw copies, so the
// binary embeds ~600 kB where the tree holds 2.7 MB and the server never compresses at
// request time. The Go handler (internal/web) finds the `.gz` sibling and negotiates;
// a client that refuses gzip gets the bytes decompressed on the fly.
//
// Only `dist/assets/` is touched: those names are content-hashed and immutable. The
// shell and root files stay raw — they are tiny and revalidated, not cached forever.
import { readdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { extname, join } from "node:path";
import { gzipSync } from "node:zlib";

const dir = join(import.meta.dirname, "..", "dist", "assets");

// Compressible text formats. Fonts and images ship pre-compressed by their own formats
// and would only grow.
const COMPRESS = new Set([".js", ".mjs", ".css", ".svg", ".json", ".map"]);

// Below this, the gzip header eats the savings and a second file is pure noise.
const MIN_BYTES = 1024;

let raw = 0;
let stored = 0;
let kept = 0;
for (const name of readdirSync(dir)) {
  const file = join(dir, name);
  if (!COMPRESS.has(extname(name))) {
    kept += 1;
    continue;
  }
  const buf = readFileSync(file);
  if (buf.length < MIN_BYTES) {
    kept += 1;
    continue;
  }
  const gz = gzipSync(buf, { level: 9 });
  writeFileSync(`${file}.gz`, gz);
  unlinkSync(file);
  raw += buf.length;
  stored += gz.length;
}

const kb = (n) => `${(n / 1024).toFixed(0)} kB`;
console.log(`dist/assets compressed: ${kb(raw)} -> ${kb(stored)} stored (${kept} file(s) kept raw)`);
