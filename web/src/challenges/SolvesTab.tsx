import { useQuery } from "@tanstack/react-query";
import { challengeSolvesQuery, meQuery } from "../queries";
import { Badge, EmptyState, RelativeTime, Skeleton } from "../ui";
import { QueryError } from "./QueryError";
import { useClock } from "./clock";

/**
 * Who solved this, oldest first — so the first row is the first blood.
 *
 * Past the freeze the server truncates this list for anyone without the exemption. The page-level
 * banner says so; here it only changes what "empty" means, because "nobody has solved this" and
 * "nobody had solved this before the freeze" are different facts.
 */
export function SolvesTab({ challengeId }: { challengeId: number }) {
  const solves = useQuery(challengeSolvesQuery(challengeId));
  const me = useQuery(meQuery);
  const { frozen } = useClock();

  const truncated = frozen && !(me.data?.is_admin ?? false);

  if (solves.isPending) return <Skeleton lines={5} height="1.5rem" />;
  if (solves.isError) {
    return <QueryError error={solves.error} onRetry={() => void solves.refetch()} />;
  }

  const rows = solves.data.solves ?? [];

  return (
    <>
      {rows.length === 0 ? (
        <EmptyState
          title="no solves yet"
          description={
            truncated
              ? "nobody had solved this before the freeze."
              : "nobody has solved this one. first blood is still on the table."
          }
        />
      ) : (
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
      )}
    </>
  );
}
</content>
