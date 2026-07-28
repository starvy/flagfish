import { useQuery } from "@tanstack/react-query";
import { challengesQuery } from "../queries";
import { EmptyState } from "../ui";
import { BoardSkeleton } from "./BoardSkeleton";
import { ChallengeCard } from "./ChallengeCard";
import { ClockBanners } from "./Banners";
import { QueryError } from "./QueryError";
import type { BoardChallenge } from "./types";
import "./challenges.css";

/**
 * The board.
 *
 * There is deliberately no route loader: a denial (unverified, teamless, not started) must reach
 * <PolicyGate>, which knows the reason vocabulary, rather than the router's error boundary, which
 * would render a stack trace where a countdown belongs.
 */
export function Board() {
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
