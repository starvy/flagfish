import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute, getRouteApi } from "@tanstack/react-router";
import type { Team } from "../../api/client";
import { meQuery, scoreHistoryQuery, teamQuery } from "../../queries";
import { ScreenGate } from "../../scoreboard/states";
import { ScoreChart } from "../../scoreboard/ScoreChart";
import {
  Badge,
  Card,
  DataTable,
  EmptyState,
  RelativeTime,
  Skeleton,
  type Column,
} from "../../ui";
import "../../scoreboard/screens.css";

export const Route = createFileRoute("/_auth/teams/$teamId")({
  params: {
    parse: (raw) => ({ teamId: Number(raw.teamId) }),
    stringify: ({ teamId }) => ({ teamId: String(teamId) }),
  },
  // No loader: in users mode this route does not exist for the caller and the server says so with
  // a 404. That is a denial to render, not an error for the router to swallow.
  component: TeamProfilePage,
});

const authRoute = getRouteApi("/_auth");

type Member = NonNullable<Team["members"]>[number];

function TeamProfilePage() {
  const { teamId } = Route.useParams();
  const { me: cachedMe } = authRoute.useRouteContext();
  const { data: me } = useQuery({ ...meQuery, initialData: cachedMe });

  const team = useQuery(teamQuery(teamId));
  const history = useQuery(scoreHistoryQuery(teamId));
  const members = team.data?.members ?? [];
  // The server withholds solves under score_visibility exactly as it withholds the score, handing
  // back an empty list rather than a partial one — so the solve card rides the same gate as the score.
  const solves = team.data?.solves ?? [];
  // A null score is the server withholding it: score_visibility hides the figures while the roster
  // (who is on the team) stays visible. Each member's points/solve_count come back null too, and the
  // score-over-time endpoint is gated the same way, so there is nothing to chart.
  const scoresHidden = team.data?.score === null;

  const columns: Column<Member>[] = [
    {
      key: "name",
      header: "member",
      cell: (m) => (
        <span className="ff-row">
          <span>{m.name}</span>
          {m.captain && <Badge tone="accent">captain</Badge>}
          {m.user_id === me.user_id && <Badge tone="neutral">you</Badge>}
        </span>
      ),
    },
    {
      key: "solves",
      header: "solves",
      align: "right",
      width: "7rem",
      cell: (m) => m.solve_count ?? "—",
    },
    {
      key: "points",
      header: "points",
      align: "right",
      width: "8rem",
      className: "ff-board__score",
      cell: (m) => m.points ?? "—",
    },
  ];

  return (
    <>
      <div className="page-head">
        <h1>team</h1>
        <Link to="/scoreboard" className="muted">
          back to the scoreboard
        </Link>
      </div>

      <ScreenGate error={team.error} onRetry={() => void team.refetch()}>
        {team.isPending || team.data === undefined ? (
          <Skeleton lines={3} height="1.5rem" />
        ) : (
          <>
            <div className="ff-team-head">
              <h2>{team.data.name}</h2>
              <span className="ff-team-score">
                {scoresHidden ? <Badge tone="neutral">scores hidden</Badge> : `${team.data.score} pts`}
              </span>
            </div>

            <div className="ff-team-meta">
              {team.data.affiliation !== undefined && <span>{team.data.affiliation}</span>}
              {team.data.country !== undefined && <span>{team.data.country}</span>}
              {team.data.website !== undefined && (
                <a href={team.data.website} rel="noreferrer noopener nofollow" target="_blank">
                  {team.data.website}
                </a>
              )}
              <span>
                formed <RelativeTime value={team.data.created_at} />
              </span>
            </div>

            <div className="ff-stack">
              {scoresHidden ? (
                <Card title="Score">
                  <EmptyState
                    title="Scores are hidden"
                    description="This instance is not publishing scores right now. The roster is below."
                  />
                </Card>
              ) : (
                <Card title="Score over time">
                  <ScoreChart points={history.data?.points ?? []} subject={team.data.name} />
                </Card>
              )}

              <DataTable
                columns={columns}
                rows={members}
                rowKey={(m) => m.user_id}
                // Points follow the member who earned them: each solve stamped the account it was
                // attributed to, so leaving or joining a team does not rewrite anybody's total.
                caption="Roster — solves and points as they were attributed when each flag landed"
                captionHidden={false}
                rowClassName={(m) => (m.user_id === me.user_id ? "ff-board__row--self" : undefined)}
                empty={
                  <EmptyState
                    title="Nobody is on this team"
                    description="Every member has left. The team keeps the points its solves earned."
                  />
                }
              />

              {!scoresHidden && (
                <Card title="Solves">
                  {solves.length === 0 ? (
                    <EmptyState
                      title="No solves yet"
                      description="The first flag this team lands shows up here."
                    />
                  ) : (
                    <ol className="ff-timeline">
                      {solves.map((s) => (
                        <li key={`${s.challenge_id}-${s.date}`} className="ff-timeline__item">
                          <Link
                            to="/challenges/$challengeId"
                            params={{ challengeId: s.challenge_id }}
                            className="ff-timeline__name"
                          >
                            {s.challenge_name}
                          </Link>
                          <span className="ff-timeline__value muted">{s.value} pts</span>
                          <RelativeTime value={s.date} className="ff-timeline__when" />
                        </li>
                      ))}
                    </ol>
                  )}
                </Card>
              )}
            </div>
          </>
        )}
      </ScreenGate>
    </>
  );
}
