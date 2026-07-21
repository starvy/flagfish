import { useId, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import { adminStatsQuery } from "../../../queries";
import type { AdminStats, StatsBucket } from "../../../api/admin";
import { AdminPage, ErrorState, LoadingState, Stat, StatGrid } from "../../../admin";
import { denialOf, PolicyGate } from "../../../policy";
import { Badge, Card, EmptyState, Select, formatAbsolute } from "../../../ui";
import { areaPath, fractionsOfMax, linePath, niceMax, project, proportions } from "../../../lib/chart";

export const Route = createFileRoute("/_auth/admin/stats")({
  component: StatsPage,
});

type ChallengeSolve = NonNullable<AdminStats["challenge_solves"]>[number];
type TypeCount = NonNullable<AdminStats["submissions_by_type"]>[number];
type TimeBucket = NonNullable<AdminStats["solves_over_time"]>[number];

const BUCKETS: ReadonlyArray<{ value: StatsBucket; label: string }> = [
  { value: "hour", label: "by hour" },
  { value: "day", label: "by day" },
  { value: "week", label: "by week" },
  { value: "month", label: "by month" },
];

type SortKey = "name" | "category" | "value" | "solve_count";

function StatsPage() {
  const [bucket, setBucket] = useState<StatsBucket>("day");
  const q = useQuery(adminStatsQuery({ bucket }));

  if (q.error !== null && denialOf(q.error) !== null) return <PolicyGate error={q.error} />;

  const bucketPicker = (
    <label className="ff-field">
      <span className="ff-sr-only">Timeline granularity</span>
      <Select
        value={bucket}
        onChange={(e) => setBucket(e.target.value as StatsBucket)}
        options={BUCKETS.map((b) => ({ value: b.value, label: b.label }))}
      />
    </label>
  );

  return (
    <AdminPage
      title="Statistics"
      description="Where the points went, what got attempted, and how the solves came in over time."
      actions={bucketPicker}
    >
      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.isPending || q.data === undefined ? (
        <LoadingState rows={6} />
      ) : (
        <StatsBody stats={q.data} />
      )}
    </AdminPage>
  );
}

function StatsBody({ stats }: { stats: AdminStats }) {
  const totals = stats.totals;
  const byType = stats.submissions_by_type ?? [];
  const challenges = stats.challenge_solves ?? [];
  const timeline = stats.solves_over_time ?? [];

  return (
    <>
      <StatGrid>
        <Stat label="Solves" value={totals.solves} />
        <Stat label="Submissions" value={totals.submissions} />
        <Stat label="Challenges solved" value={totals.solved_challenges} />
        <Stat label="Manual awards" value={totals.awards} />
      </StatGrid>

      <div className="admin-grid">
        <Card title="Submissions by verdict">
          <TypeBreakdown rows={byType} />
        </Card>

        <Card title="Solves over time">
          <SolvesTimeline buckets={timeline} bucket={stats.bucket} />
        </Card>
      </div>

      <Card title="Solves per challenge" flush>
        <ChallengeTable rows={challenges} />
      </Card>
    </>
  );
}

const TYPE_LABEL: Record<string, string> = {
  correct: "correct",
  incorrect: "incorrect",
  partial: "partial",
  discard: "discard",
  ratelimited: "rate-limited",
};

function TypeBreakdown({ rows }: { rows: readonly TypeCount[] }) {
  if (rows.length === 0) {
    return <EmptyState title="Nothing submitted yet" description="Verdicts appear here once players start attempting flags." />;
  }
  const shares = proportions(rows.map((r) => r.count));
  return (
    <ul className="admin-breakdown">
      {rows.map((r, i) => (
        <li key={r.type} className="admin-breakdown__row">
          <span className="admin-breakdown__label">{TYPE_LABEL[r.type] ?? r.type}</span>
          <span className="admin-bar" aria-hidden="true">
            <span
              className={`admin-bar__fill admin-bar__fill--${r.type}`}
              style={{ width: `${Math.round(shares[i] * 100)}%` }}
            />
          </span>
          <span className="admin-breakdown__count">
            {r.count} <span className="muted">({Math.round(shares[i] * 100)}%)</span>
          </span>
        </li>
      ))}
    </ul>
  );
}

const CHART_W = 560;
const CHART_H = 160;
const CHART_PAD = 6;

function SolvesTimeline({ buckets, bucket }: { buckets: readonly TimeBucket[]; bucket: string }) {
  const captionId = useId();
  const total = buckets.reduce((s, b) => s + b.count, 0);

  if (buckets.length === 0 || total === 0) {
    return (
      <EmptyState
        title="No solves to plot"
        description="Once flags start landing, this fills in with a solve count per interval."
      />
    );
  }

  const data = buckets.map((b) => ({ x: Date.parse(b.bucket), y: b.count }));
  const yMax = niceMax(Math.max(...buckets.map((b) => b.count)));
  const projected = project(data, { width: CHART_W, height: CHART_H, pad: CHART_PAD, yMax });
  const baseline = CHART_H - CHART_PAD;

  return (
    <figure className="admin-timechart" aria-labelledby={captionId}>
      <svg
        className="admin-timechart__svg"
        viewBox={`0 0 ${CHART_W} ${CHART_H}`}
        preserveAspectRatio="none"
        role="img"
        aria-labelledby={captionId}
      >
        <path className="admin-timechart__area" d={areaPath(projected, baseline)} />
        <path className="admin-timechart__line" d={linePath(projected)} />
        {projected.map((p, i) => (
          <circle key={i} className="admin-timechart__dot" cx={p.x} cy={p.y} r={2} />
        ))}
      </svg>
      <figcaption id={captionId} className="admin-timechart__cap">
        {total} solves across {buckets.length} {bucket} intervals · peak {yMax} per interval.
      </figcaption>
      <table className="ff-sr-only">
        <caption>Solves per {bucket}</caption>
        <thead>
          <tr>
            <th scope="col">Interval start</th>
            <th scope="col">Solves</th>
          </tr>
        </thead>
        <tbody>
          {buckets.map((b) => (
            <tr key={b.bucket}>
              <td>{formatAbsolute(new Date(b.bucket))}</td>
              <td>{b.count}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}

function ChallengeTable({ rows }: { rows: readonly ChallengeSolve[] }) {
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({
    key: "solve_count",
    dir: "desc",
  });

  const sorted = useMemo(() => {
    const dir = sort.dir === "asc" ? 1 : -1;
    return [...rows].sort((a, b) => {
      const av = a[sort.key];
      const bv = b[sort.key];
      if (typeof av === "number" && typeof bv === "number") return (av - bv) * dir;
      return String(av).localeCompare(String(bv)) * dir;
    });
  }, [rows, sort]);

  const maxSolves = Math.max(0, ...rows.map((r) => r.solve_count));
  const fractions = fractionsOfMax(sorted.map((r) => r.solve_count));

  if (rows.length === 0) {
    return (
      <div className="ff-card__body">
        <EmptyState title="No challenges yet" description="Add a challenge with a flag and its solve count shows up here." />
      </div>
    );
  }

  const toggle = (key: SortKey) =>
    setSort((s) =>
      s.key === key
        ? { key, dir: s.dir === "asc" ? "desc" : "asc" }
        : { key, dir: key === "name" || key === "category" ? "asc" : "desc" },
    );

  const arrow = (key: SortKey) => (sort.key === key ? (sort.dir === "asc" ? " ▲" : " ▼") : "");
  const ariaSort = (key: SortKey): "ascending" | "descending" | "none" =>
    sort.key !== key ? "none" : sort.dir === "asc" ? "ascending" : "descending";

  return (
    <table className="ff-table admin-solvetable">
      <caption className="ff-sr-only">Solve count per challenge, zero-solve challenges included</caption>
      <thead>
        <tr>
          <th scope="col" aria-sort={ariaSort("name")}>
            <SortButton onClick={() => toggle("name")}>challenge{arrow("name")}</SortButton>
          </th>
          <th scope="col" aria-sort={ariaSort("category")}>
            <SortButton onClick={() => toggle("category")}>category{arrow("category")}</SortButton>
          </th>
          <th scope="col" className="admin-solvetable__num" aria-sort={ariaSort("value")}>
            <SortButton onClick={() => toggle("value")}>value{arrow("value")}</SortButton>
          </th>
          <th scope="col" aria-sort={ariaSort("solve_count")}>
            <SortButton onClick={() => toggle("solve_count")}>solves{arrow("solve_count")}</SortButton>
          </th>
        </tr>
      </thead>
      <tbody>
        {sorted.map((c, i) => (
          <tr key={c.challenge_id}>
            <td>
              <Link to="/admin/challenges/$challengeId" params={{ challengeId: String(c.challenge_id) }}>
                {c.name}
              </Link>
            </td>
            <td>
              <Badge tone="neutral">{c.category}</Badge>
            </td>
            <td className="admin-solvetable__num">{c.value}</td>
            <td>
              <span className="admin-solvebar">
                <span className="admin-bar" aria-hidden="true">
                  <span className="admin-bar__fill" style={{ width: `${Math.round(fractions[i] * 100)}%` }} />
                </span>
                <span className="admin-solvebar__count">
                  {c.solve_count}
                  {c.solve_count === maxSolves && maxSolves > 0 && (
                    <>
                      {" "}
                      <span className="muted">top</span>
                    </>
                  )}
                </span>
              </span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function SortButton({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" className="admin-sort" onClick={onClick}>
      {children}
    </button>
  );
}
