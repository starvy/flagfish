import { describe, expect, it } from "vitest";
import { parseInline, parseMarkdown, safeHref, type Block, type Inline } from "./markdownAst";

const text = (value: string): Inline => ({ type: "text", value });

describe("safeHref", () => {
  it.each(["https://ctf.example/x", "http://a.b", "mailto:ops@example.com", "/files/1", "#hint"])(
    "allows %s",
    (href) => {
      expect(safeHref(href)).toBe(href);
    },
  );

  it.each([
    "javascript:alert(1)",
    "JavaScript:alert(1)",
    "  javascript:alert(1)  ",
    "java\tscript:alert(1)",
    "java\nscript:alert(1)",
    "data:text/html;base64,PHNjcmlwdD4=",
    "vbscript:msgbox",
    "//evil.example/steal",
    "",
  ])("rejects %j", (href) => {
    expect(safeHref(href)).toBeNull();
  });
});

describe("parseInline", () => {
  it("keeps raw HTML as text — there is no HTML rule", () => {
    expect(parseInline("<script>alert(1)</script>")).toEqual([text("<script>alert(1)</script>")]);
    expect(parseInline('<img src=x onerror="alert(1)">')).toEqual([
      text('<img src=x onerror="alert(1)">'),
    ]);
  });

  it("strips the anchor from an unsafe scheme but keeps the label", () => {
    expect(parseInline("[click me](javascript:alert(1))")).toEqual([text("click me")]);
  });

  it("renders a safe link", () => {
    expect(parseInline("see [the brief](https://ctf.example/brief) now")).toEqual([
      text("see "),
      { type: "link", href: "https://ctf.example/brief", children: [text("the brief")] },
      text(" now"),
    ]);
  });

  it("parses bold, italic and code", () => {
    expect(parseInline("**bold** and *it* and `x*y`")).toEqual([
      { type: "strong", children: [text("bold")] },
      text(" and "),
      { type: "em", children: [text("it")] },
      text(" and "),
      { type: "code", value: "x*y" },
    ]);
  });

  it("leaves underscores inside identifiers alone", () => {
    expect(parseInline("flag_format_here")).toEqual([text("flag_format_here")]);
    expect(parseInline("_emphatic_")).toEqual([{ type: "em", children: [text("emphatic")] }]);
  });

  it("honours backslash escapes", () => {
    expect(parseInline("\\*not bold\\*")).toEqual([text("*not bold*")]);
  });

  it("treats an unterminated marker as text", () => {
    expect(parseInline("2 * 3 * 4 is `unclosed")).toEqual([text("2 * 3 * 4 is `unclosed")]);
  });
});

describe("parseMarkdown", () => {
  it("parses headings, paragraphs and lists", () => {
    const blocks = parseMarkdown("# Title\n\nSome text\nwrapped.\n\n- one\n- two\n\n1. a\n2. b");
    expect(blocks).toEqual<Block[]>([
      { type: "heading", level: 1, children: [text("Title")] },
      { type: "paragraph", children: [text("Some text wrapped.")] },
      { type: "list", ordered: false, items: [[text("one")], [text("two")]] },
      { type: "list", ordered: true, items: [[text("a")], [text("b")]] },
    ]);
  });

  it("keeps fenced code verbatim, markup and all", () => {
    const blocks = parseMarkdown("```sh\nnc **host** 1337\n<b>hi</b>\n```");
    expect(blocks).toEqual<Block[]>([
      { type: "code", lang: "sh", value: "nc **host** 1337\n<b>hi</b>" },
    ]);
  });

  it("closes an unterminated fence at end of input", () => {
    expect(parseMarkdown("```\nx")).toEqual<Block[]>([{ type: "code", value: "x" }]);
  });

  it("parses a blockquote, recursively", () => {
    expect(parseMarkdown("> quoted **hard**")).toEqual<Block[]>([
      {
        type: "blockquote",
        children: [
          {
            type: "paragraph",
            children: [text("quoted "), { type: "strong", children: [text("hard")] }],
          },
        ],
      },
    ]);
  });

  it("bounds nesting on hostile input", () => {
    expect(() => parseMarkdown(">".repeat(5000) + " deep")).not.toThrow();
    expect(() => parseInline("*".repeat(2000) + "x")).not.toThrow();
  });

  it("returns nothing for empty input", () => {
    expect(parseMarkdown("")).toEqual([]);
    expect(parseMarkdown("\n\n  \n")).toEqual([]);
  });
});
