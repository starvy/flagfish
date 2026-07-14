import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, Outlet, createFileRoute } from "@tanstack/react-router";
import type { ChallengeListItem } from "../../api/client";
import { challengesQuery } from "../../queries";

export const Route = createFileRoute("/_auth/challenges")({
  loader: ({ context }) => context.queryClient.ensureQueryData(challengesQuery),
  component: BoardPage,
});

function groupByCategory(items: ChallengeListItem[]): Map<string, ChallengeListItem[]> {
  const groups = new Map<string, ChallengeListItem[]>();
  for (const c of items) {
    const list = groups.get(c.category) ?? [];
    list.push(c);
    groups.set(c.category, list);
  }
  for (const list of groups.values()) {
    list.sort((a, b) => a.value - b.value || a.name.localeCompare(b.name));
  }
  return new Map([...groups.entries()].sort(([a], [b]) => a.localeCompare(b)));
}

function BoardPage() {
  const { data } = useSuspenseQuery(challengesQuery);
  const groups = groupByCategory(data.challenges ?? []);

  return (
    <>
      {groups.size === 0 && <div className="center">no challenges yet — check back soon</div>}
      {[...groups.entries()].map(([category, items]) => (
        <section className="category" key={category}>
          <h2>{category}</h2>
          <div className="card-grid">
            {items.map((c) => (
              <Link
                key={c.id}
                to="/challenges/$challengeId"
                params={{ challengeId: c.id }}
                className={c.solved ? "chal-card solved" : "chal-card"}
              >
                <span className="name">{c.name}</span>
                <span className="meta">
                  <span className="points">{c.value} pts</span>
                  <span>{c.solve_count} solves</span>
                </span>
              </Link>
            ))}
          </div>
        </section>
      ))}
      <Outlet />
    </>
  );
}
