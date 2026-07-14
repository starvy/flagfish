import { useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, getRouteApi, useNavigate } from "@tanstack/react-router";
import type { Standing } from "../../api/client";
import { PolicyNotice, type Denial } from "../../policy";
import { bracketsQuery, instanceQuery, meQuery, qk, scoreboardQuery } from "../../queries";
import { BracketFilter } from "../../scoreboard/BracketFilter";
import { TimeTravel } from "../../scoreboard/TimeTravel";
import { clockOf, hasEnded, isFrozen } from "../../scoreboard/clock";
import { Countdown, ScreenGate, useNow } from "../../scoreboard/states";
import { Badge, Banner, DataTable, EmptyState, formatAbsolute, type Column } from "../../ui";
import "../../scoreboard/screens.css";

export interface BoardSearch {
  /** Absent is the overall board. The API rejects anything below 1. */
  bracket?: number;
  /** RFC 3339. The standings as they stood at that instant. */
  as_of?: string;
}

function bracketParam(raw: unknown): number | undefined {
  const id = Number(raw);
  return Number.isInteger(id) && id >= 1 ? id : undefined;
}

function asOfParam(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  const at = new Date(raw);
  return Number.isNaN(at.getTime()) ? undefined : at.toISOString();
}

export const Route = createFileRoute("/_auth/scoreboard")({
  validateSearch: (search: Record<string, unknown>): BoardSearch => ({
    bracket: bracketParam(search.bracket),
    as_of: asOfParam(search.as_of),
  }),
  // No loader: a denial here — hidden scores, a board that does not exist for this viewer — is a
  // state this page renders, not an error for the router to catch.
  component: ScoreboardPage,
});

const authRoute = getRouteApi("/_auth");

// The clock only has to be right to the minute: it decides whether the freeze banner is up and
// where the right edge of the scrub range sits. Polling the board is what keeps the rows current.
const CLOCK_TICK = 30_000;

function ScoreboardPage() {
  const { bracket, as_of } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const queryClient = useQueryClient();
  const { me: cachedMe } = authRoute.useRouteContext();

  const board = useQuery(scoreboardQuery({ bracket, as_of }));
  const brackets = useQuery(bracketsQuery);
  const instance = useQuery(instanceQuery);
  const { data: me } = useQuery({ ...meQuery, initialData: cachedMe });

  const now = useNow(CLOCK_TICK);
  const clock = clockOf(instance.data);
  const teams = clock.mode === "teams";
  const frozen = isFrozen(clock, now);
  const ended = hasEnded(clock, now);

  // In users mode an account is the player; in teams mode it is the team. The board speaks
  // account ids either way, so one comparison finds the viewer's row in both.
  const selfAccount = me.team_id ?? me.user_id;

  const setSearch = useCallback(
    (next: Partial<BoardSearch>) => {
      void navigate({ search: (prev) => ({ ...prev, ...next }), replace: true });
    },
    [navigate],
  );

  const started = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: qk.instance() });
    void queryClient.invalidateQueries({ queryKey: qk.scoreboard() });
  }, [queryClient]);

  const denied = (denial: Denial) =>
    denial.reason === "ctf-not-started" ? (
      <Countdown denial={denial} start={clock.start} onStarted={started} />
    ) : (
      <PolicyNotice denial={denial} />
    );

  const standings = board.data?.standings ?? [];
  const bracketName = brackets.data?.brackets?.find((b) => b.id === bracket)?.name;

  const columns: Column<Standing>[] = [
    {
      key: "rank",
      header: "#",
      align: "right",
      width: "4rem",
      className: "ff-board__rank",
      cell: (s) => s.rank,
    },
    {
      key: "name",
      header: teams ? "team" : "player",
      cell: (s) => (
        <span className="ff-row">
          {teams ? (
            <Link to="/teams/$teamId" params={{ teamId: s.account_id }}>
              {s.name}
            </Link>
          ) : (
            <span>{s.name}</span>
          )}
          {s.account_id === selfAccount && <Badge tone="accent">you</Badge>}
        </span>
      ),
    },
    {
      key: "score",
      header: "score",
      align: "right",
      width: "8rem",
      className: "ff-board__score",
      cell: (s) => s.score,
    },
  ];

  return (
    <>
      <div className="page-head">
        <h1>scoreboard</h1>
        {board.isFetching && <span className="muted">updating…</span>}
      </div>

      {frozen && clock.freeze !== null && (
        <Banner tone="warn" title="Standings are frozen">
          The board stopped moving at {formatAbsolute(clock.freeze)}. Solves after that are not
          shown until the organisers lift the freeze.
        </Banner>
      )}

      {ended && clock.end !== null && !frozen && (
        <Banner tone="info" title="The CTF has ended">
          These are the final standings, as of {formatAbsolute(clock.end)}.
        </Banner>
      )}

      <ScreenGate error={board.error} onRetry={() => void board.refetch()} fallback={denied}>
        <div className="ff-board-toolbar">
          {(brackets.data?.brackets?.length ?? 0) > 0 && (
            <BracketFilter
              brackets={brackets.data?.brackets ?? []}
              value={bracket}
              onChange={(next) => setSearch({ bracket: next })}
            />
          )}

          {/* Nothing to scrub through before the event opens, and nothing to scrub through when
              the organisers never set a start. */}
          {clock.start !== null && now >= clock.start.getTime() && (
            <TimeTravel
              start={clock.start}
              latest={new Date(clock.end === null ? now : Math.min(now, clock.end.getTime()))}
              value={as_of}
              onChange={(next) => setSearch({ as_of: next })}
            />
          )}
        </div>

        <DataTable
          columns={columns}
          rows={standings}
          rowKey={(s) => s.account_id}
          caption={caption(bracketName, as_of)}
          captionHidden={false}
          loading={board.isPending}
          skeletonRows={10}
          rowClassName={(s) => (s.account_id === selfAccount ? "ff-board__row--self" : undefined)}
          empty={
            <EmptyState
              title="Nobody has scored yet"
              description={
                as_of === undefined
                  ? "The first solve puts someone here."
                  : "No one had scored by the instant you are looking at."
              }
            />
          }
        />
      </ScreenGate>
    </>
  );
}

function caption(bracketName: string | undefined, asOf: string | undefined): string {
  const scope = bracketName === undefined ? "Overall standings" : `Standings — ${bracketName}`;
  return asOf === undefined ? scope : `${scope}, as of ${formatAbsolute(new Date(asOf))}`;
}
