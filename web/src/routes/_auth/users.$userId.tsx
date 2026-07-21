import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute, getRouteApi } from "@tanstack/react-router";
import { meQuery, scoreHistoryQuery, userProfileQuery } from "../../queries";
import { ScreenGate } from "../../scoreboard/states";
import { ScoreChart } from "../../scoreboard/ScoreChart";
import { Badge, Card, EmptyState, RelativeTime, Skeleton } from "../../ui";
import "../../scoreboard/screens.css";

export const Route = createFileRoute("/_auth/users/$userId")({
  params: {
    parse: (raw) => ({ userId: Number(raw.userId) }),
    stringify: ({ userId }) => ({ userId: String(userId) }),
  },
  // No loader: a hidden or banned account answers 404, which is a denial the page renders rather than
  // an error for the router to swallow.
  component: UserProfilePage,
});

const authRoute = getRouteApi("/_auth");

function UserProfilePage() {
  const { userId } = Route.useParams();
  const { me: cachedMe } = authRoute.useRouteContext();
  const { data: me } = useQuery({ ...meQuery, initialData: cachedMe });

  const profile = useQuery(userProfileQuery(userId));
  const history = useQuery(scoreHistoryQuery(userId));
  const solves = profile.data?.solves ?? [];
  const isSelf = me.user_id === userId;
  // A null score is the server withholding it: score_visibility hides the figures while leaving the
  // account itself visible. The score-over-time endpoint is gated the same way and simply 404s, so
  // there is nothing to chart either.
  const scoresHidden = profile.data?.score === null;

  return (
    <>
      <div className="page-head">
        <h1>player</h1>
        <Link to="/scoreboard" className="muted">
          back to the scoreboard
        </Link>
      </div>

      <ScreenGate error={profile.error} onRetry={() => void profile.refetch()}>
        {profile.isPending || profile.data === undefined ? (
          <Skeleton lines={3} height="1.5rem" />
        ) : (
          <>
            <div className="ff-team-head">
              <h2>
                <span className="ff-row">
                  {profile.data.name}
                  {isSelf && <Badge tone="accent">you</Badge>}
                </span>
              </h2>
              <span className="ff-team-score">
                {scoresHidden ? <Badge tone="neutral">scores hidden</Badge> : `${profile.data.score} pts`}
              </span>
            </div>

            <div className="ff-team-meta">
              {profile.data.bracket_name !== undefined && (
                <Badge tone="neutral">{profile.data.bracket_name}</Badge>
              )}
              {profile.data.affiliation !== undefined && <span>{profile.data.affiliation}</span>}
              {profile.data.country !== undefined && <span>{profile.data.country}</span>}
              {profile.data.website !== undefined && (
                <a href={profile.data.website} rel="noreferrer noopener nofollow" target="_blank">
                  {profile.data.website}
                </a>
              )}
              <span>
                joined <RelativeTime value={profile.data.created_at} />
              </span>
            </div>

            {scoresHidden ? (
              <Card title="Score">
                <EmptyState
                  title="Scores are hidden"
                  description="This instance is not publishing scores or solve history right now."
                />
              </Card>
            ) : (
              <>
                <Card title="Score over time">
                  <ScoreChart points={history.data?.points ?? []} subject={profile.data.name} />
                </Card>

                <Card title="Solves">
                  {solves.length === 0 ? (
                    <EmptyState
                      title="No solves yet"
                      description="The first flag this player lands shows up here."
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
              </>
            )}
          </>
        )}
      </ScreenGate>
    </>
  );
}
