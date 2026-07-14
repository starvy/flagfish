import type { ReactNode } from "react";
import { useQueries } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import {
  adminBracketsQuery,
  adminConfigQuery,
  adminTagsQuery,
  adminUsersQuery,
  challengesQuery,
  instanceQuery,
} from "../../../queries";
import {
  AdminPage,
  Def,
  DefList,
  ErrorState,
  LoadingState,
  Stat,
  StatGrid,
  localZone,
} from "../../../admin";
import { Alert, Badge, Card, EmptyState, RelativeTime, formatAbsolute } from "../../../ui";

export const Route = createFileRoute("/_auth/admin/")({
  component: OverviewPage,
});

type Phase = "unscheduled" | "before" | "running" | "frozen" | "ended";

// The clock as the players live it. `paused` is not a phase — a paused event is still running,
// it just refuses flags — so it rides alongside this rather than overwriting it.
function phaseOf(
  now: number,
  start: string | undefined,
  end: string | undefined,
  freeze: string | undefined,
): Phase {
  const at = (iso: string | undefined) => (iso ? Date.parse(iso) : Number.NaN);
  const [s, e, f] = [at(start), at(end), at(freeze)];

  if (Number.isNaN(s) && Number.isNaN(e)) return "unscheduled";
  if (!Number.isNaN(s) && now < s) return "before";
  if (!Number.isNaN(e) && now >= e) return "ended";
  if (!Number.isNaN(f) && now >= f) return "frozen";
  return "running";
}

const PHASE: Record<Phase, { label: string; tone: "neutral" | "accent" | "warn" }> = {
  unscheduled: { label: "no clock set", tone: "neutral" },
  before: { label: "not started", tone: "warn" },
  running: { label: "running", tone: "accent" },
  frozen: { label: "running · frozen", tone: "warn" },
  ended: { label: "ended", tone: "neutral" },
};

function OverviewPage() {
  const results = useQueries({
    queries: [
      instanceQuery,
      adminConfigQuery,
      challengesQuery,
      // The roster is not the point here: one row of page one is the cheapest read of `total`.
      adminUsersQuery({ page: 1, per_page: 1 }),
      adminBracketsQuery,
      adminTagsQuery,
    ],
  });

  const [instance, config, challenges, users, brackets, tags] = results;
  const error = results.find((r) => r.error)?.error ?? null;
  const refetchAll = () => {
    for (const r of results) void r.refetch();
  };

  if (error !== null) {
    return (
      <Shell>
        <ErrorState error={error} onRetry={refetchAll} />
      </Shell>
    );
  }

  if (!instance.data || !config.data || !challenges.data) {
    return (
      <Shell>
        <LoadingState rows={6} />
      </Shell>
    );
  }

  const inst = instance.data;
  const cfg = config.data;
  const list = challenges.data.challenges;
  const phase = phaseOf(Date.now(), cfg.start, cfg.end, cfg.freeze);
  const categories = new Set(list.map((c) => c.category)).size;
  const points = list.reduce((sum, c) => sum + c.value, 0);

  return (
    <Shell>
      {inst.paused && (
        <Alert tone="warn" title="The CTF is paused">
          Submissions are refused for everyone, admins included. Browsing, hint unlocks and the
          board keep working.
        </Alert>
      )}

      <StatGrid>
        <Stat
          label="Event"
          value={PHASE[phase].label}
          tone={PHASE[phase].tone}
          hint={
            phase === "before" && cfg.start ? (
              <>
                starts <RelativeTime value={cfg.start} />
              </>
            ) : phase === "running" && cfg.end ? (
              <>
                ends <RelativeTime value={cfg.end} />
              </>
            ) : phase === "unscheduled" ? (
              "open until you set one"
            ) : undefined
          }
        />
        <Stat label="Challenges" value={list.length} hint={`${categories} categories`} />
        <Stat label="Points on the board" value={points} />
        <Stat
          label={inst.mode === "teams" ? "Users (teams mode)" : "Users"}
          value={users.data?.total ?? "—"}
        />
        <Stat label="Brackets" value={brackets.data?.brackets.length ?? "—"} />
        <Stat label="Tags" value={tags.data?.tags.length ?? "—"} />
      </StatGrid>

      <div className="admin-grid">
        <Card title="The clock" actions={<Badge tone="neutral">{localZone()}</Badge>}>
          <DefList>
            <Def term="Start">
              <Instant iso={cfg.start} absent="not set — the CTF is open from the moment it exists" />
            </Def>
            <Def term="End">
              <Instant iso={cfg.end} absent="not set — the CTF never closes" />
            </Def>
            <Def term="Freeze">
              <Instant iso={cfg.freeze} absent="not set — the board is live to the last second" />
            </Def>
            <Def term="Paused">
              {inst.paused ? <Badge tone="warn">yes</Badge> : <span className="muted">no</span>}
            </Def>
          </DefList>
          <p>
            <Link to="/admin/config">Edit the clock →</Link>
          </p>
        </Card>

        <Card title="Visibility">
          <DefList>
            <Def term="Challenges">
              <Badge tone="neutral">{cfg.challenge_visibility}</Badge>
            </Def>
            <Def term="Scores">
              <Badge tone={cfg.score_visibility === "hidden" ? "warn" : "neutral"}>
                {cfg.score_visibility}
              </Badge>
            </Def>
            <Def term="Accounts">
              <Badge tone="neutral">{cfg.account_visibility}</Badge>
            </Def>
            <Def term="Registration">
              <Badge tone={cfg.registration_visibility === "public" ? "neutral" : "warn"}>
                {cfg.registration_visibility}
              </Badge>
            </Def>
            <Def term="Team creation">
              {inst.team_creation ? "on" : <span className="muted">off</span>}
            </Def>
            <Def term="Email verification">
              {inst.verify_emails ? "required" : <span className="muted">off</span>}
            </Def>
          </DefList>
        </Card>

        <Card title="Instance">
          <DefList>
            <Def term="Name">{cfg.name}</Def>
            <Def term="Description">{cfg.description || <span className="muted">none</span>}</Def>
            <Def term="Mode">
              <Badge tone="accent">{inst.mode}</Badge>{" "}
              <span className="muted">fixed at setup</span>
            </Def>
            <Def term="Default theme">
              <span className="ff-mono">{cfg.theme}</span>
            </Def>
          </DefList>
        </Card>

        <Card title="Go to">
          <nav className="admin-links" aria-label="Console shortcuts">
            <Link to="/admin/config">Config</Link>
            <Link to="/admin/challenges">Challenges</Link>
            <Link to="/admin/tags">Tags</Link>
            <Link to="/admin/users">Users</Link>
            <Link to="/admin/brackets">Brackets</Link>
            <Link to="/admin/notifications">Notifications</Link>
            <Link to="/admin/audit">Audit</Link>
            <Link to="/admin/anticheat">Anticheat</Link>
          </nav>
        </Card>
      </div>

      {list.length === 0 && (
        <EmptyState
          title="No challenges yet"
          description="Nothing is playable until this instance has a challenge with a flag on it."
          action={<Link to="/admin/challenges">Create the first one</Link>}
        />
      )}
    </Shell>
  );
}

function Shell({ children }: { children: ReactNode }) {
  return (
    <AdminPage
      title="Overview"
      description="This instance as the players see it, and the shortest way to each knob that changes it."
    >
      {children}
    </AdminPage>
  );
}

function Instant({ iso, absent }: { iso: string | undefined; absent: string }) {
  if (!iso) return <span className="muted">{absent}</span>;
  const at = new Date(iso);
  return (
    <>
      <RelativeTime value={at} /> <span className="muted">({formatAbsolute(at)})</span>
    </>
  );
}
