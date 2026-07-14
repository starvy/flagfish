import { useQuery } from "@tanstack/react-query";
import { Outlet, createFileRoute, useChildMatches } from "@tanstack/react-router";
import { challengesQuery } from "../../queries";
import { EmptyState, Skeleton } from "../../ui";
import { ChallengeCard } from "../../challenges/ChallengeCard";
import { ClockBanners } from "../../challenges/Banners";
import { QueryError } from "../../challenges/QueryError";
import type { BoardChallenge } from "../../challenges/types";
import "../../challenges/challenges.css";

export const Route = createFileRoute("/_auth/challenges")({
  component: ChallengesRoute,
});

// `/challenges/$id` nests under this route in the file tree, but it is a page of its own, not a
// panel inside the board. When a child matches, the board steps out of its way.
function ChallengesRoute() {
  const children = useChildMatches();
  return children.length > 0 ? <Outlet /> : <Board />;
}

/**
 * The board.
 *
 * There is deliberately no route loader: a denial (unverified, teamless, not started) must reach
 * <PolicyGate>, which knows the reason vocabulary, rather than the router's error boundary, which
 * would render a stack trace where a countdown belongs.
 */
function Board() {
  const board = useQuery(challengesQuery);

  return (
    <>
      <div className="page-head">
        <h1>challenges</h1>
      </div>

      <ClockBanners />

      {board.isPending && <BoardSkeleton />}

      {board.isError && <QueryError error={board.error} onRetry={() => void board.refetch()} />}

      {board.isSuccess && <Categories challenges={board.data.challenges ?? []} />}
    </>
  );
}

function Categories({ challenges }: { challenges: BoardChallenge[] }) {
  if (challenges.length === 0) {
    return (
      <EmptyState
        title="no challenges yet"
        description="the organisers have not published any. they appear here the moment they do."
      />
    );
  }

  return (
    <>
      {[...groupByCategory(challenges)].map(([category, items]) => (
        <section className="board__category" key={category}>
          <div className="board__category-head">
            <h2 className="board__category-name">{category}</h2>
            <span className="muted">
              {items.filter((c) => c.solved).length}/{items.length}
            </span>
          </div>
          <div className="board__grid">
            {items.map((challenge) => (
              <ChallengeCard key={challenge.id} challenge={challenge} />
            ))}
          </div>
        </section>
      ))}
    </>
  );
}

function groupByCategory(items: BoardChallenge[]): Map<string, BoardChallenge[]> {
  const groups = new Map<string, BoardChallenge[]>();
  for (const c of items) {
    const list = groups.get(c.category) ?? [];
    list.push(c);
    groups.set(c.category, list);
  }
  for (const list of groups.values()) {
    list.sort((a, b) => a.value - b.value || a.name.localeCompare(b.name));
  }
  return new Map([...groups].sort(([a], [b]) => a.localeCompare(b)));
}

// A skeleton in the shape of the board, not a spinner on white: the page that arrives should be
// the page that was promised.
function BoardSkeleton() {
  return (
    <>
      {[0, 1].map((section) => (
        <section className="board__category" key={section}>
          <div className="board__category-head">
            <Skeleton width="8rem" height="1.25rem" />
          </div>
          <div className="board__grid">
            {[0, 1, 2, 3].map((card) => (
              <div className="ff-card chal-card" key={card}>
                <Skeleton height="1.25rem" />
                <Skeleton width="60%" height="0.875rem" />
              </div>
            ))}
          </div>
        </section>
      ))}
    </>
  );
}
