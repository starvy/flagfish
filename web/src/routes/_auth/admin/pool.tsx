import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import { poolStatsQuery } from "../../../queries";
import { AdminPage, AsyncState } from "../../../admin";
import { Badge, Card, EmptyState } from "../../../ui";
import type { PoolStat } from "../../../api/admin";

export const Route = createFileRoute("/_auth/admin/pool")({
  component: PoolPage,
});

// A pool this full is close to stranding its next registrant. The gauge exists so an organiser sees
// that before the doors open, not from a 503 mid-event.
const NEAR_EXHAUSTION = 0.9;

function PoolPage() {
  const stats = useQuery(poolStatsQuery());
  const pools = stats.data?.pools ?? [];

  return (
    <AdminPage
      title="Instance pools"
      description="Utilisation for every unique-flag challenge. Exhaustion mid-event is a hard 503, so the fix is prevention: fill a pool that is running hot before the doors open."
    >
      <AsyncState
        loading={stats.isPending}
        error={stats.isError ? stats.error : null}
        onRetry={() => void stats.refetch()}
        isEmpty={pools.length === 0}
        empty={
          <EmptyState
            title="No pools yet"
            description="A challenge appears here once it is in unique-flag mode and has instances uploaded. Switch a challenge to unique on its editor, then upload a pool."
          />
        }
      >
        <Card flush>
          <ul className="pool-gauges">
            {pools.map((p) => (
              <PoolGauge key={p.challenge_id} pool={p} />
            ))}
          </ul>
        </Card>
      </AsyncState>
    </AdminPage>
  );
}

function PoolGauge({ pool }: { pool: PoolStat }) {
  const pct = Math.round(pool.utilization * 100);
  const remaining = pool.total - pool.issued;
  const hot = pool.utilization >= NEAR_EXHAUSTION;

  return (
    <li className="pool-gauge">
      <div className="pool-gauge__head">
        <Link
          to="/admin/challenges/$challengeId"
          params={{ challengeId: String(pool.challenge_id) }}
          className="pool-gauge__name"
        >
          {pool.name}
        </Link>
        {hot && (
          <Badge tone={remaining === 0 ? "danger" : "warn"}>
            {remaining === 0 ? "exhausted" : "nearly full"}
          </Badge>
        )}
      </div>
      <div
        className="pool-gauge__track"
        role="meter"
        aria-valuenow={pool.issued}
        aria-valuemin={0}
        aria-valuemax={pool.total}
        aria-label={`${pool.name}: ${pool.issued} of ${pool.total} instances issued`}
      >
        <div
          className={hot ? "pool-gauge__fill pool-gauge__fill--hot" : "pool-gauge__fill"}
          style={{ width: `${pct}%` }}
        />
      </div>
      <div className="pool-gauge__meta">
        <span className="ff-mono">
          {pool.issued}/{pool.total} issued
        </span>
        <span className="ff-muted">{remaining} free</span>
      </div>
    </li>
  );
}
