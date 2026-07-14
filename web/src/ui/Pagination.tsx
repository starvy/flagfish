import { cx } from "./cx";
import { Button } from "./Button";
import { Select } from "./Select";

export interface PaginationProps {
  /** 1-based. */
  page: number;
  perPage: number;
  /** Total rows, when the endpoint reports it. Without it, `hasNext` drives Next. */
  total?: number;
  hasNext?: boolean;
  onPageChange: (page: number) => void;
  onPerPageChange?: (perPage: number) => void;
  perPageOptions?: readonly number[];
  className?: string;
}

const DEFAULT_PER_PAGE = [25, 50, 100] as const;

export function Pagination({
  page,
  perPage,
  total,
  hasNext,
  onPageChange,
  onPerPageChange,
  perPageOptions = DEFAULT_PER_PAGE,
  className,
}: PaginationProps) {
  const pages = total === undefined ? undefined : Math.max(1, Math.ceil(total / perPage));
  const next = hasNext ?? (pages === undefined ? false : page < pages);
  const first = (page - 1) * perPage;
  const status =
    total === undefined
      ? `page ${page}`
      : total === 0
        ? "0 results"
        : `${first + 1}–${Math.min(first + perPage, total)} of ${total}`;

  return (
    <nav className={cx("ff-pagination", className)} aria-label="Pagination">
      {onPerPageChange && (
        <label className="ff-pagination__per-page">
          rows
          <Select
            value={String(perPage)}
            // A new row count moves every page boundary, so the old page number means
            // nothing — go back to the first.
            onChange={(e) => {
              onPerPageChange(Number(e.target.value));
              onPageChange(1);
            }}
            options={perPageOptions.map((n) => ({ value: String(n), label: String(n) }))}
            aria-label="Rows per page"
          />
        </label>
      )}
      <span className="ff-pagination__status" aria-live="polite">
        {status}
      </span>
      <span className="ff-pagination__nav">
        <Button
          size="sm"
          variant="ghost"
          onClick={() => onPageChange(page - 1)}
          disabled={page <= 1}
        >
          ← Prev
        </Button>
        <Button size="sm" variant="ghost" onClick={() => onPageChange(page + 1)} disabled={!next}>
          Next →
        </Button>
      </span>
    </nav>
  );
}
