import { useEffect, useRef, useState } from "react";
import { cx } from "./cx";
import { Button } from "./Button";

export interface CodeBlockProps {
  code: string;
  /** Shown in the title bar. Purely a label — nothing is highlighted. */
  language?: string;
  title?: string;
  /** Wrap long lines instead of scrolling. For a one-line token or a URL. */
  wrap?: boolean;
  maxHeight?: string;
  className?: string;
}

export function CodeBlock({
  code,
  language,
  title,
  wrap = false,
  maxHeight,
  className,
}: CodeBlockProps) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    } catch {
      // The clipboard is permission-gated and absent over plain http. The text is on
      // screen and selectable; a failed copy does not deserve an error state.
      setCopied(false);
    }
  };

  const head = title ?? language;

  const button = (
    <Button
      variant="ghost"
      size="sm"
      onClick={copy}
      className={head === undefined ? "ff-code__copy--float" : undefined}
    >
      {copied ? "copied ✓" : "copy"}
    </Button>
  );

  return (
    <div className={cx("ff-code", className)}>
      {head === undefined ? (
        button
      ) : (
        <div className="ff-code__head">
          <span>{head}</span>
          {button}
        </div>
      )}
      <pre
        className={cx("ff-code__pre", wrap && "ff-code__pre--wrap")}
        style={maxHeight === undefined ? undefined : { maxHeight }}
        tabIndex={0}
      >
        <code>{code}</code>
      </pre>
      {/* The button's label changes on copy, but a screen reader is not watching it. */}
      <span className="ff-sr-only" role="status" aria-live="polite">
        {copied ? "Copied to clipboard" : ""}
      </span>
    </div>
  );
}
