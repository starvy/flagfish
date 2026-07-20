import { useEffect, useRef, type ReactNode, type RefObject } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
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

/** An imperative handle a screen holds to steer the virtualized body — e.g. a "jump to my row". */
export interface VirtualScrollHandle {
  /** Bring the row with this key into view. No-op if it is not in the current rows. */
  scrollToKey: (key: string | number) => void;
}

export interface VirtualOptions {
  /**
   * Below this many rows the plain table renders unchanged, so a small board looks and behaves
   * exactly as it did before virtualization existed. Windowing only engages once a body is large
   * enough for the DOM cost to matter.
   */
  threshold?: number;
  /** First-paint row-height guess; the real height is measured once a row mounts. */
  estimateRowHeight?: number;
  /** Rows kept mounted above and below the viewport, so a fast scroll never flashes blank. */
  overscan?: number;
  /** CSS max-height of the scroll viewport that bounds the windowed body. */
  maxHeight?: string;
  /** Filled with a handle while windowing is engaged; nulled otherwise. */
  handleRef?: RefObject<VirtualScrollHandle | null>;
}

const DEFAULT_THRESHOLD = 50;
const DEFAULT_ROW_HEIGHT = 44;
const DEFAULT_OVERSCAN = 12;
const DEFAULT_MAX_HEIGHT = "70vh";

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
  /**
   * Opt into row virtualization for long bodies. Transparent below `threshold`; above it, only the
   * visible window mounts inside a bounded, sticky-header scroll region. Admin tables leave this off
   * and render exactly as before.
   */
  virtual?: VirtualOptions;
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
  virtual,
  className,
}: DataTableProps<Row>) {
  const showEmpty = !loading && rows.length === 0;
  // Windowing is a render concern only: it engages once the body is big enough to be worth it, and
  // never while loading (skeletons are their own fixed, small set).
  const virtualized =
    virtual !== undefined && !loading && rows.length >= (virtual.threshold ?? DEFAULT_THRESHOLD);

  const head = (
    <thead>
      <tr aria-rowindex={virtualized ? 1 : undefined}>
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
  );

  const foot = pagination && (
    <div className="ff-table__foot">
      <Pagination {...pagination} />
    </div>
  );

  if (virtualized) {
    return (
      <div className={cx("ff-table-wrap", className)}>
        <VirtualTable
          columns={columns}
          rows={rows}
          rowKey={rowKey}
          caption={caption}
          captionHidden={captionHidden}
          onRowClick={onRowClick}
          rowClassName={rowClassName}
          dense={dense}
          head={head}
          options={virtual}
        />
        {foot}
      </div>
    );
  }

  return (
    <div className={cx("ff-table-wrap", className)}>
      <div className="ff-table-scroll">
        <table
          className={cx("ff-table", dense && "ff-table--dense", stickyHeader && "ff-table--sticky")}
          // The busy flag is what tells a screen reader the skeleton rows are not data.
          aria-busy={loading || undefined}
        >
          <caption className={captionHidden ? "ff-sr-only" : undefined}>{caption}</caption>
          {head}
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

      {foot}
    </div>
  );
}

interface VirtualTableProps<Row> {
  columns: readonly Column<Row>[];
  rows: readonly Row[];
  rowKey: (row: Row, index: number) => string | number;
  caption: string;
  captionHidden: boolean;
  onRowClick?: (row: Row) => void;
  rowClassName?: (row: Row) => string | undefined;
  dense: boolean;
  head: ReactNode;
  options: VirtualOptions;
}

function VirtualTable<Row>({
  columns,
  rows,
  rowKey,
  caption,
  captionHidden,
  onRowClick,
  rowClassName,
  dense,
  head,
  options,
}: VirtualTableProps<Row>) {
  const scrollRef = useRef<HTMLDivElement>(null);

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => options.estimateRowHeight ?? DEFAULT_ROW_HEIGHT,
    overscan: options.overscan ?? DEFAULT_OVERSCAN,
  });

  const items = virtualizer.getVirtualItems();
  const total = virtualizer.getTotalSize();
  // Real `<tr>`s carry the whole body's height via spacer rows above and below the window, so table
  // layout, column sizing, and native row semantics all stay intact — no absolute positioning.
  const padTop = items.length > 0 ? items[0].start : 0;
  const padBottom = items.length > 0 ? total - items[items.length - 1].end : 0;

  // Expose a "jump to a row" handle: scroll it into the window, then land focus on it so a keyboard
  // user is taken there too, not just the mouse.
  const handleRef = options.handleRef;
  useEffect(() => {
    if (handleRef === undefined) return;
    handleRef.current = {
      scrollToKey: (key) => {
        const index = rows.findIndex((row, i) => rowKey(row, i) === key);
        if (index < 0) return;
        virtualizer.scrollToIndex(index, { align: "center" });
        requestAnimationFrame(() => {
          const el = scrollRef.current?.querySelector<HTMLTableRowElement>(
            `tr[data-index="${index}"]`,
          );
          if (el === null || el === undefined) return;
          el.tabIndex = -1;
          el.focus({ preventScroll: true });
        });
      },
    };
    return () => {
      handleRef.current = null;
    };
  }, [handleRef, rows, rowKey, virtualizer]);

  return (
    <div
      ref={scrollRef}
      className="ff-table-scroll ff-table-scroll--virtual"
      style={{ maxHeight: options.maxHeight ?? DEFAULT_MAX_HEIGHT }}
    >
      <table
        className={cx("ff-table", dense && "ff-table--dense", "ff-table--sticky")}
        // The DOM holds only the visible window; these tell AT the true size and each row's place
        // in it, so a screen reader announces "row 900 of 4000" and not "row 3 of 20".
        aria-rowcount={rows.length + 1}
      >
        <caption className={captionHidden ? "ff-sr-only" : undefined}>{caption}</caption>
        {head}
        <tbody>
          {padTop > 0 && (
            <tr aria-hidden="true" className="ff-table__spacer">
              <td colSpan={columns.length} style={{ height: padTop, padding: 0, border: 0 }} />
            </tr>
          )}

          {items.map((item) => {
            const row = rows[item.index];
            return (
              <tr
                key={rowKey(row, item.index)}
                data-index={item.index}
                ref={virtualizer.measureElement}
                aria-rowindex={item.index + 2}
                className={cx(onRowClick && "ff-table__row--clickable", rowClassName?.(row))}
                onClick={onRowClick && (() => onRowClick(row))}
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
                    {c.cell(row, item.index)}
                  </td>
                ))}
              </tr>
            );
          })}

          {padBottom > 0 && (
            <tr aria-hidden="true" className="ff-table__spacer">
              <td colSpan={columns.length} style={{ height: padBottom, padding: 0, border: 0 }} />
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

function alignClass(align: ColumnAlign | undefined): string | undefined {
  return align === undefined || align === "left" ? undefined : `ff-table__cell--${align}`;
}
