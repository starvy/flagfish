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

/**
 * A team's solve log, if the instance publishes one.
 *
 * The public team body does not carry solves today, so this reads structurally and the section
 * stays hidden when the field is absent. Absent is not the same as empty: a team that has scored
 * but whose log is not published must not be shown an empty timeline claiming it never solved
 * anything.
 */
interface TeamSolve {
  challenge_id: number;
  challenge_name: string;
  value: number;
  first_blood: boolean;
  date: string;
}

function isSolve(row: unknown): row is TeamSolve {
  if (typeof row !== "object" || row === null) return false;
  const s = row as Record<string, unknown>;
  return (
    typeof s.challenge_id === "number" &&
    typeof s.challenge_name === "string" &&
    typeof s.value === "number" &&
    typeof s.first_blood === "boolean" &&
    typeof s.date === "string"
  );
}

function solvesOf(team: Team | undefined): readonly TeamSolve[] | null {
  const wire = (team ?? {}) as { solves?: unknown };
  if (!Array.isArray(wire.solves)) return null;
  return wire.solves.every(isSolve) ? (wire.solves as TeamSolve[]) : null;
}

function TeamProfilePage() {
  const { teamId } = Route.useParams();
  const { me: cachedMe } = authRoute.useRouteContext();
  const { data: me } = useQuery({ ...meQuery, initialData: cachedMe });

  const team = useQuery(teamQuery(teamId));
  const history = useQuery(scoreHistoryQuery(teamId));
  const members = team.data?.members ?? [];
  const solves = solvesOf(team.data);

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
      cell: (m) => m.solve_count,
    },
    {
      key: "points",
      header: "points",
      align: "right",
      width: "8rem",
      className: "ff-board__score",
      cell: (m) => m.points,
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
              <span className="ff-team-score">{team.data.score} pts</span>
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
              <Card title="Score over time">
                <ScoreChart points={history.data?.points ?? []} subject={team.data.name} />
              </Card>

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

              {solves !== null && (
                <Card title="Solves">
                  {solves.length === 0 ? (
                    <EmptyState
                      title="No solves yet"
                      description="The first flag this team lands shows up here."
                    />
                  ) : (
                    <ol className="ff-timeline">
                      {solves.map((s) => (
                        <li
                          key={`${s.challenge_id}-${s.date}`}
                          className={`ff-timeline__item${s.first_blood ? " ff-timeline__item--blood" : ""}`}
                        >
                          <Link
                            to="/challenges/$challengeId"
                            params={{ challengeId: s.challenge_id }}
                            className="ff-timeline__name"
                          >
                            {s.challenge_name}
                          </Link>
                          {s.first_blood && <Badge tone="blood">first blood</Badge>}
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
