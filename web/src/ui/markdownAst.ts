/*
 * A deliberately small Markdown subset.
 *
 * Challenge descriptions and notification bodies are operator input rendered to every
 * player, so this is a security boundary. It is built so injection is structurally
 * impossible rather than scrubbed after the fact:
 *
 *   - The parser emits an AST, never an HTML string. Markdown.tsx turns that into
 *     React elements, so every text node is escaped by React and there is no
 *     dangerouslySetInnerHTML anywhere on the path.
 *   - There is no HTML rule. `<script>` is a `<` followed by the word script: it
 *     parses as text, and it renders as text.
 *   - A link becomes an anchor only if its scheme is on the allowlist. Anything else
 *     — javascript:, data:, vbscript: — keeps its label and loses its href.
 *
 * The subset: headings, paragraphs, fenced code, blockquote, ordered and unordered
 * lists, bold, italic, inline code, links. Everything else is text.
 */

export type Inline =
  | { type: "text"; value: string }
  | { type: "code"; value: string }
  | { type: "strong"; children: Inline[] }
  | { type: "em"; children: Inline[] }
  | { type: "link"; href: string; children: Inline[] };

export type Block =
  | { type: "heading"; level: 1 | 2 | 3 | 4 | 5 | 6; children: Inline[] }
  | { type: "paragraph"; children: Inline[] }
  | { type: "code"; lang?: string; value: string }
  | { type: "list"; ordered: boolean; items: Inline[][] }
  | { type: "blockquote"; children: Block[] };

const SAFE_SCHEMES = ["http:", "https:", "mailto:"];
const SCHEME = /^[a-zA-Z][a-zA-Z0-9+.-]*:/;

// Everything a browser can be talked into executing sits behind a scheme, so an
// allowlist on the scheme is the whole of the check. A URL with no scheme at all is
// relative — same-origin by construction, and safe.
export function safeHref(raw: string): string | null {
  const href = raw.trim();
  if (href === "") return null;
  if (hasControlChar(href)) return null;
  if (SCHEME.test(href)) {
    const scheme = href.slice(0, href.indexOf(":") + 1).toLowerCase();
    return SAFE_SCHEMES.includes(scheme) ? href : null;
  }
  // Protocol-relative (//evil.example) is a scheme in disguise: it inherits ours.
  if (href.startsWith("//")) return null;
  return href;
}

// Control characters and inner whitespace exist in a URL only to smuggle something
// past a check — a browser strips them, so "java\tscript:" arrives as javascript:.
// Reject rather than normalise: a URL that needs one is not a URL.
function hasControlChar(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const code = s.charCodeAt(i);
    if (code <= 0x20 || code === 0x7f) return true;
  }
  return false;
}

const HEADING = /^(#{1,6})\s+(.*)$/;
const FENCE = /^```\s*([\w+-]*)\s*$/;
const FENCE_END = /^```\s*$/;
const UL_ITEM = /^\s{0,3}[-*+]\s+(.*)$/;
const OL_ITEM = /^\s{0,3}\d{1,9}[.)]\s+(.*)$/;
const QUOTE = /^\s{0,3}>\s?(.*)$/;
const MAX_DEPTH = 4;

export function parseMarkdown(src: string, depth = 0): Block[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i]!;

    if (line.trim() === "") {
      i++;
      continue;
    }

    const fence = FENCE.exec(line);
    if (fence) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !FENCE_END.test(lines[i]!)) body.push(lines[i++]!);
      i++; // the closing fence, or the end of the input
      const lang = fence[1];
      blocks.push({ type: "code", value: body.join("\n"), ...(lang ? { lang } : {}) });
      continue;
    }

    const heading = HEADING.exec(line);
    if (heading) {
      const level = heading[1]!.length as 1 | 2 | 3 | 4 | 5 | 6;
      blocks.push({ type: "heading", level, children: parseInline(heading[2]!) });
      i++;
      continue;
    }

    if (QUOTE.test(line)) {
      const body: string[] = [];
      while (i < lines.length && QUOTE.test(lines[i]!)) body.push(QUOTE.exec(lines[i++]!)![1]!);
      // A quote nests, so the parser recurses — with a bound, because the input is
      // untrusted and a thousand `>` is otherwise a stack overflow.
      const children = depth < MAX_DEPTH ? parseMarkdown(body.join("\n"), depth + 1) : [];
      blocks.push({ type: "blockquote", children });
      continue;
    }

    if (UL_ITEM.test(line) || OL_ITEM.test(line)) {
      const ordered = !UL_ITEM.test(line);
      const re = ordered ? OL_ITEM : UL_ITEM;
      const items: Inline[][] = [];
      while (i < lines.length) {
        const m = re.exec(lines[i]!);
        if (!m) break;
        items.push(parseInline(m[1]!));
        i++;
      }
      blocks.push({ type: "list", ordered, items });
      continue;
    }

    const para: string[] = [];
    while (i < lines.length) {
      const next = lines[i]!;
      if (
        next.trim() === "" ||
        HEADING.test(next) ||
        FENCE.test(next) ||
        QUOTE.test(next) ||
        UL_ITEM.test(next) ||
        OL_ITEM.test(next)
      ) {
        break;
      }
      para.push(next.trim());
      i++;
    }
    blocks.push({ type: "paragraph", children: parseInline(para.join(" ")) });
  }

  return blocks;
}

const PUNCT = /[\\`*_[\]()#+\-.!>]/;
const WORD = /\w/;
const SPACE = /\s/;

export function parseInline(src: string, depth = 0): Inline[] {
  const out: Inline[] = [];
  let text = "";
  let i = 0;

  const flush = () => {
    if (text !== "") {
      out.push({ type: "text", value: text });
      text = "";
    }
  };

  while (i < src.length) {
    const ch = src[i]!;

    if (ch === "\\" && i + 1 < src.length && PUNCT.test(src[i + 1]!)) {
      text += src[i + 1];
      i += 2;
      continue;
    }

    // Code first, and it never recurses: the point of a code span is that what is
    // inside it is not markup. A flag with asterisks in it comes through intact.
    if (ch === "`") {
      const end = src.indexOf("`", i + 1);
      if (end > i + 1) {
        flush();
        out.push({ type: "code", value: src.slice(i + 1, end) });
        i = end + 1;
        continue;
      }
    }

    if (ch === "[") {
      const link = matchLink(src, i);
      if (link) {
        flush();
        const href = safeHref(link.href);
        const label: Inline[] =
          depth < MAX_DEPTH
            ? parseInline(link.label, depth + 1)
            : [{ type: "text", value: link.label }];
        // A rejected scheme loses the anchor, not the words: the reader still sees
        // what the operator wrote, they just cannot be made to run it.
        out.push(
          href === null
            ? { type: "text", value: link.label }
            : { type: "link", href, children: label },
        );
        i = link.end;
        continue;
      }
    }

    if (ch === "*" || ch === "_") {
      const strong = src.startsWith(ch + ch, i);
      const marker = strong ? ch + ch : ch;
      const end = findCloser(src, i + marker.length, marker, ch === "_");
      if (end !== -1 && canOpen(src, i, marker)) {
        flush();
        const inner = src.slice(i + marker.length, end);
        const children: Inline[] =
          depth < MAX_DEPTH ? parseInline(inner, depth + 1) : [{ type: "text", value: inner }];
        out.push({ type: strong ? "strong" : "em", children });
        i = end + marker.length;
        continue;
      }
    }

    text += ch;
    i++;
  }

  flush();
  return out;
}

// The flanking rule, and it is what keeps prose readable: an opener must run straight
// into its content. Without it "2 * 3 * 4" is an italic 3, and every underscore in
// flag_format_here opens emphasis.
function canOpen(src: string, at: number, marker: string): boolean {
  const after = src[at + marker.length];
  if (after === undefined || SPACE.test(after)) return false;
  if (!marker.startsWith("_")) return true;
  const before = src[at - 1];
  return before === undefined || !WORD.test(before);
}

function findCloser(src: string, from: number, marker: string, underscore: boolean): number {
  let at = from;
  while (at < src.length) {
    const found = src.indexOf(marker, at);
    if (found === -1 || found === from) return -1; // empty emphasis stays literal

    const before = src[found - 1]!;
    const after = src[found + marker.length];
    const escaped = before === "\\";
    const dangling = SPACE.test(before);
    const intraword = underscore && after !== undefined && WORD.test(after);

    if (escaped || dangling || intraword) {
      at = found + marker.length;
      continue;
    }
    return found;
  }
  return -1;
}

interface LinkMatch {
  label: string;
  href: string;
  end: number;
}

function matchLink(src: string, at: number): LinkMatch | null {
  let depth = 0;
  let close = -1;
  for (let i = at; i < src.length; i++) {
    if (src[i] === "\\") {
      i++;
      continue;
    }
    if (src[i] === "[") depth++;
    else if (src[i] === "]") {
      depth--;
      if (depth === 0) {
        close = i;
        break;
      }
    }
  }
  if (close === -1 || src[close + 1] !== "(") return null;

  // Balance the parens rather than stopping at the first `)`: javascript:alert(1) has
  // one inside it, and cutting the URL short would hand safeHref a different string
  // from the one the browser would see.
  let open = 0;
  let end = -1;
  for (let i = close + 1; i < src.length; i++) {
    if (src[i] === "(") open++;
    else if (src[i] === ")" && --open === 0) {
      end = i;
      break;
    }
  }
  if (end === -1) return null;

  return { label: src.slice(at + 1, close), href: src.slice(close + 2, end), end: end + 1 };
}
