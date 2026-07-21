import { useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { challengeSolvesQuery, meQuery } from "../queries";
import { Badge, Button, EmptyState, RelativeTime, Skeleton } from "../ui";
import { QueryError } from "./QueryError";
import { useClock } from "./clock";

/**
 * Who solved this, oldest first — so the first row is the first blood.
 *
 * The server bounds the list: one keyset page at a time, resumed by the opaque cursor the previous
 * page returns — the same shape the admin submissions feed uses. It also withholds the list wholesale
 * when the instance hides who its players are, in which case there are simply no rows and no cursor,
 * and this reads as an ordinary empty state rather than a broken one.
 *
 * Past the freeze the server truncates the list for anyone without the exemption. The page-level
 * banner says so; here it only changes what "empty" means, because "nobody has solved this" and
 * "nobody had solved this before the freeze" are different facts.
 */
export function SolvesTab({ challengeId }: { challengeId: number }) {
  const me = useQuery(meQuery);
  const { frozen } = useClock();

  // One query per loaded page, keyed by the cursor that opens it; undefined is the first page.
  const [cursors, setCursors] = useState<Array<string | undefined>>([undefined]);
  const results = useQueries({
    queries: cursors.map((cursor) => challengeSolvesQuery(challengeId, cursor)),
  });

  const firstError = results.find((r) => r.isError);
  if (firstError?.error != null) {
    return <QueryError error={firstError.error} onRetry={() => void firstError.refetch()} />;
  }
  if (results.some((r) => r.isPending)) return <Skeleton lines={5} height="1.5rem" />;

  // Oldest-first order is the server's; concatenating pages preserves it, so the global index is the
  // solve rank and index 0 is the genuine first blood. Dedupe defensively across page boundaries.
  const rows: { name: string; value: number; date: string }[] = [];
  const seen = new Set<string>();
  for (const r of results) {
    for (const s of r.data?.solves ?? []) {
      const key = `${s.name}-${s.date}`;
      if (!seen.has(key)) {
        seen.add(key);
        rows.push(s);
      }
    }
  }

  const nextCursor = results[results.length - 1]?.data?.next_cursor ?? "";
  const loadMore = () => {
    if (nextCursor !== "") setCursors((cs) => [...cs, nextCursor]);
  };

  const truncated = frozen && !(me.data?.is_admin ?? false);

  if (rows.length === 0) {
    return (
      <EmptyState
        title="no solves yet"
        description={
          truncated
            ? "nobody had solved this before the freeze."
            : "nobody has solved this one. first blood is still on the table."
        }
      />
    );
  }

  return (
    <>
      <div className="ff-table-wrap">
        <table className="ff-table ff-table--dense">
          <caption className="ff-sr-only">solves for this challenge, earliest first</caption>
          <thead>
            <tr>
              <th scope="col">#</th>
              <th scope="col">who</th>
              <th scope="col">points</th>
              <th scope="col">when</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((solve, i) => (
              <tr key={`${solve.name}-${solve.date}`} className={i === 0 ? "solve-list__blood" : undefined}>
                <td className="solve-list__rank">{i + 1}</td>
                <td>
                  {solve.name}{" "}
                  {i === 0 && (
                    <Badge tone="blood" aria-label="first blood">
                      first blood
                    </Badge>
                  )}
                </td>
                <td>{solve.value}</td>
                <td>
                  <RelativeTime value={solve.date} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {nextCursor !== "" && (
        <div className="solve-list__more">
          <Button size="sm" variant="ghost" onClick={loadMore}>
            load more
          </Button>
        </div>
      )}
    </>
  );
}
