import { Fragment, useMemo, type JSX, type ReactNode } from "react";
import { cx } from "./cx";
import { parseMarkdown, type Block, type Inline } from "./markdownAst";

export interface MarkdownProps {
  source: string;
  className?: string;
}

/**
 * Renders the restricted Markdown subset (see markdown.ts) as React elements. There is
 * no HTML passthrough and no dangerouslySetInnerHTML: the parser hands over an AST and
 * every leaf is a string, which React escapes.
 */
export function Markdown({ source, className }: MarkdownProps) {
  const blocks = useMemo(() => parseMarkdown(source), [source]);

  return <div className={cx("ff-md", className)}>{blocks.map(renderBlock)}</div>;
}

function renderBlock(block: Block, key: number): ReactNode {
  switch (block.type) {
    case "heading": {
      // Operator content starts at h3: the page owns h1 and h2, and a body that could
      // outrank them would wreck the outline a screen reader navigates by.
      const level = Math.min(block.level + 2, 6);
      const Tag = `h${level}` as keyof JSX.IntrinsicElements;
      return <Tag key={key}>{block.children.map(renderInline)}</Tag>;
    }
    case "paragraph":
      return <p key={key}>{block.children.map(renderInline)}</p>;
    case "code":
      return (
        <pre key={key} tabIndex={0}>
          <code>{block.value}</code>
        </pre>
      );
    case "list": {
      const items = block.items.map((item, i) => <li key={i}>{item.map(renderInline)}</li>);
      return block.ordered ? <ol key={key}>{items}</ol> : <ul key={key}>{items}</ul>;
    }
    case "blockquote":
      return <blockquote key={key}>{block.children.map(renderBlock)}</blockquote>;
  }
}

function renderInline(node: Inline, key: number): ReactNode {
  switch (node.type) {
    case "text":
      return <Fragment key={key}>{node.value}</Fragment>;
    case "code":
      return <code key={key}>{node.value}</code>;
    case "strong":
      return <strong key={key}>{node.children.map(renderInline)}</strong>;
    case "em":
      return <em key={key}>{node.children.map(renderInline)}</em>;
    case "link":
      return (
        <a
          key={key}
          href={node.href}
          // Operator links point off-site; noopener keeps the new tab from reaching
          // back into this one through window.opener.
          target="_blank"
          rel="noopener noreferrer nofollow"
        >
          {node.children.map(renderInline)}
        </a>
      );
  }
}
