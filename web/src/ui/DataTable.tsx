import type { ReactNode } from "react";
import { cx } from "./cx";
import { EmptyState } from "./EmptyState";
import { Pagination, type PaginationProps } from "./Pagination";
import { Skeleton } from "./Skeleton";

export type ColumnAlign = "left" | "right" | "center";

export interface Column<Row> {
  /** Unique within the column set; also the React key for the cell. */
  key: string;
  header: ReactNode;
  cell: (row: Row, index: number) => ReactNode;
  align?: ColumnAlign;
  /** Any CSS width — `6rem`, `20%`. Omit to let the column size itself. */
  width?: string;
  /** The header is redundant on screen (an actions column) but still named for AT. */
  headerHidden?: boolean;
  className?: string;
}

export type PaginationState = Omit<PaginationProps, "className">;

export interface DataTableProps<Row> {
  columns: readonly Column<Row>[];
  rows: readonly Row[];
  rowKey: (row: Row, index: number) => string | number;
  /** Names the table for screen readers. Required — an unnamed table is a grid of noise. */
  caption: string;
  /** Hide the caption visually; it stays in the accessibility tree. */
  captionHidden?: boolean;
  loading?: boolean;
  skeletonRows?: number;
  /** Shown instead of the body when `rows` is empty and we are not loading. */
  empty?: ReactNode;
  pagination?: PaginationState;
  onRowClick?: (row: Row) => void;
  rowClassName?: (row: Row) => string | undefined;
  dense?: boolean;
  stickyHeader?: boolean;
  className?: string;
}

export function DataTable<Row>({
  columns,
  rows,
  rowKey,
  caption,
  captionHidden = true,
  loading = false,
  skeletonRows = 5,
  empty,
  pagination,
  onRowClick,
  rowClassName,
  dense = false,
  stickyHeader = false,
  className,
}: DataTableProps<Row>) {
  const showEmpty = !loading && rows.length === 0;

  return (
    <div className={cx("ff-table-wrap", className)}>
      <div className="ff-table-scroll">
        <table
          className={cx("ff-table", dense && "ff-table--dense", stickyHeader && "ff-table--sticky")}
          // The busy flag is what tells a screen reader the skeleton rows are not data.
          aria-busy={loading || undefined}
        >
          <caption className={captionHidden ? "ff-sr-only" : undefined}>{caption}</caption>
          <thead>
            <tr>
              {columns.map((c) => (
                <th
                  key={c.key}
                  scope="col"
                  style={c.width === undefined ? undefined : { width: c.width }}
                  className={cx(alignClass(c.align), c.className)}
                >
                  {c.headerHidden ? <span className="ff-sr-only">{c.header}</span> : c.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {loading &&
              Array.from({ length: skeletonRows }, (_, r) => (
                <tr key={`skeleton-${r}`}>
                  {columns.map((c) => (
                    <td key={c.key} className={cx(alignClass(c.align), c.className)}>
                      <Skeleton />
                    </td>
                  ))}
                </tr>
              ))}

            {showEmpty && (
              <tr className="ff-table__empty">
                <td colSpan={columns.length}>{empty ?? <EmptyState title="Nothing here yet" />}</td>
              </tr>
            )}

            {!loading &&
              rows.map((row, i) => (
                <tr
                  key={rowKey(row, i)}
                  className={cx(onRowClick && "ff-table__row--clickable", rowClassName?.(row))}
                  onClick={onRowClick && (() => onRowClick(row))}
                  // A clickable row is a control: it has to be reachable and firable
                  // from the keyboard, not only under a mouse.
                  tabIndex={onRowClick ? 0 : undefined}
                  onKeyDown={
                    onRowClick &&
                    ((e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        onRowClick(row);
                      }
                    })
                  }
                >
                  {columns.map((c) => (
                    <td key={c.key} className={cx(alignClass(c.align), c.className)}>
                      {c.cell(row, i)}
                    </td>
                  ))}
                </tr>
              ))}
          </tbody>
        </table>
      </div>

      {pagination && (
        <div className="ff-table__foot">
          <Pagination {...pagination} />
        </div>
      )}
    </div>
  );
}

function alignClass(align: ColumnAlign | undefined): string | undefined {
  return align === undefined || align === "left" ? undefined : `ff-table__cell--${align}`;
}
