import { useQuery } from "@tanstack/react-query";
import { createFileRoute, getRouteApi } from "@tanstack/react-router";
import { meQuery, scoreboardQuery } from "../../queries";

export const Route = createFileRoute("/_auth/scoreboard")({
  loader: ({ context }) => context.queryClient.ensureQueryData(scoreboardQuery),
  component: ScoreboardPage,
});

const authRoute = getRouteApi("/_auth");

function ScoreboardPage() {
  // Polls while mounted (refetchInterval on the query), so the board tracks the
  // game without a manual refresh.
  const { data, dataUpdatedAt } = useQuery(scoreboardQuery);
  const { me } = authRoute.useRouteContext();
  const { data: freshMe } = useQuery({ ...meQuery, initialData: me });

  const standings = data?.standings ?? [];
  const selfAccount = freshMe.team_id ?? freshMe.user_id;

  return (
    <>
      <div className="page-head">
        <h1>scoreboard</h1>
        <span className="muted">updated {new Date(dataUpdatedAt).toLocaleTimeString()}</span>
      </div>
      {standings.length === 0 ? (
        <div className="center">nobody has scored yet</div>
      ) : (
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>name</th>
              <th style={{ textAlign: "right" }}>score</th>
            </tr>
          </thead>
          <tbody>
            {standings.map((s) => (
              <tr key={s.account_id} className={s.account_id === selfAccount ? "self" : undefined}>
                <td className="rank">{s.rank}</td>
                <td>{s.name}</td>
                <td className="score">{s.score}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
