import { useEffect, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, notFound } from "@tanstack/react-router";
import { isApiError, type Team } from "../../api/client";
import { instanceQuery, myTeamQuery, meQuery, useCreateTeam, useJoinTeam, useLeaveTeam } from "../../queries";
import { denialOf, PolicyGate } from "../../policy";
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDestructive,
  DataTable,
  EmptyState,
  Field,
  Form,
  Input,
  RelativeTime,
  Skeleton,
  useToast,
  type Column,
} from "../../ui";

export const Route = createFileRoute("/_auth/team")({
  beforeLoad: async ({ context }) => {
    const instance = await context.queryClient.ensureQueryData(instanceQuery);
    // In users mode there are no teams, so there is no such page — not a 403 with an
    // explanation, which would only tell a player about a world they cannot enter.
    if (instance.mode !== "teams") throw notFound();
    return { instance };
  },
  component: TeamPage,
});

function TeamPage() {
  const { instance } = Route.useRouteContext();
  const { data: me } = useQuery(meQuery);
  const team = useQuery(myTeamQuery);

  if (team.isPending) {
    return (
      <>
        <div className="page-head">
          <h1>team</h1>
        </div>
        <Card>
          <Skeleton lines={4} />
        </Card>
      </>
    );
  }

  if (team.error) {
    return (
      <>
        <div className="page-head">
          <h1>team</h1>
        </div>
        <LoadFailure error={team.error} onRetry={() => void team.refetch()} />
      </>
    );
  }

  // Null is not a failure: the server answers 404 for a player who has not joined yet, and
  // that is the state this page exists to resolve.
  return team.data === null ? (
    <Enrollment teamCreation={instance.team_creation} />
  ) : (
    <MemberView team={team.data} userId={me?.user_id} />
  );
}

/* ---------------------------------------------------------------- enrollment */

function Enrollment({ teamCreation }: { teamCreation: boolean }) {
  const qc = useQueryClient();
  const create = useCreateTeam();
  const join = useJoinTeam();

  const createReason = denialOf(create.error)?.reason ?? null;
  const joinReason = denialOf(join.error)?.reason ?? null;

  // The server is the authority on enrollment, not the cached instance snapshot: a team
  // joined in another tab, or creation switched off a second ago, both land here first.
  const onTeam = createReason === "already-on-team" || joinReason === "already-on-team";
  const canCreate = teamCreation && createReason !== "team-creation-disabled";

  useEffect(() => {
    if (onTeam) void qc.invalidateQueries({ queryKey: myTeamQuery.queryKey });
  }, [onTeam, qc]);

  return (
    <>
      <div className="page-head">
        <h1>team</h1>
      </div>

      {/* A denial that carries a destination — unverified, an unfinished profile — is the
          policy layer's to act on, not a message to bury in a form. */}
      <PolicyGate error={redirecting(create.error) ?? redirecting(join.error)} />

      {onTeam ? (
        <Alert tone="info" title="You are already on a team">
          Reloading your team…
        </Alert>
      ) : (
        <div className="ff-stack">
          <p className="muted">
            This CTF is played in teams. {canCreate ? "Start one, or join an existing one with its name and password." : "Join an existing team with its name and password."}
          </p>

          {!canCreate && (
            <Alert tone="info" title="Team creation is closed">
              The organisers have turned off new teams. You can still join one.
            </Alert>
          )}

          {canCreate && <CreateTeam mutation={create} />}
          <JoinTeam mutation={join} />
        </div>
      )}
    </>
  );
}

function CreateTeam({ mutation }: { mutation: ReturnType<typeof useCreateTeam> }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    mutation.mutate(
      { name, password: password === "" ? undefined : password },
      { onSuccess: (team) => toast.success("Team created", `You are the captain of ${team.name}.`) },
    );
  };

  return (
    <Card title="Create a team">
      <Form
        onSubmit={submit}
        error={formError(mutation.error)}
        footer={
          <Button type="submit" variant="primary" loading={mutation.isPending}>
            Create team
          </Button>
        }
      >
        <Field name="name" label="Team name" required>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={128}
            autoComplete="off"
            required
          />
        </Field>
        <Field
          name="password"
          label="Join password"
          hint="Teammates need it to join. Leave it blank to let anyone in."
        >
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            maxLength={128}
            autoComplete="new-password"
          />
        </Field>
      </Form>
    </Card>
  );
}

function JoinTeam({ mutation }: { mutation: ReturnType<typeof useJoinTeam> }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    mutation.mutate(
      { name, password: password === "" ? undefined : password },
      { onSuccess: (team) => toast.success("Joined", `You are on ${team.name}.`) },
    );
  };

  // The server answers "no such team" and "wrong password" with one message on purpose:
  // branching on it here would turn the join form into a team-name oracle. Render what it
  // said, nothing more.
  return (
    <Card title="Join a team">
      <Form
        onSubmit={submit}
        error={formError(mutation.error)}
        footer={
          <Button type="submit" variant="primary" loading={mutation.isPending}>
            Join team
          </Button>
        }
      >
        <Field name="name" label="Team name" required>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={128}
            autoComplete="off"
            required
          />
        </Field>
        <Field name="password" label="Join password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            maxLength={128}
            autoComplete="off"
          />
        </Field>
      </Form>
    </Card>
  );
}

/* -------------------------------------------------------------- member view */

type TeamMember = NonNullable<Team["members"]>[number];

function MemberView({ team, userId }: { team: Team; userId: number | undefined }) {
  const toast = useToast();
  const leave = useLeaveTeam();
  const [confirming, setConfirming] = useState(false);

  const members = team.members ?? [];
  const solves = members.reduce((n, m) => n + m.solve_count, 0);
  // Points are attributed to the account at solve time, so leaving cannot rewrite history —
  // which is exactly why the server refuses to let a scored team shed a member.
  const locked = solves > 0;

  const columns: readonly Column<TeamMember>[] = [
    {
      key: "name",
      header: "Player",
      cell: (m) => (
        <span className="ff-row">
          {m.name}
          {m.captain && <Badge tone="accent">captain</Badge>}
          {m.user_id === userId && <Badge tone="neutral">you</Badge>}
        </span>
      ),
    },
    { key: "solves", header: "Solves", align: "right", width: "8rem", cell: (m) => m.solve_count },
    { key: "points", header: "Points", align: "right", width: "8rem", cell: (m) => m.points },
  ];

  return (
    <>
      <div className="page-head">
        <h1>{team.name}</h1>
        <span className="muted">
          {team.score} points · created <RelativeTime value={team.created_at} />
        </span>
      </div>

      <div className="ff-stack">
        <TeamProfile team={team} />

        <Card title="Roster" flush>
          <DataTable
            caption={`Members of ${team.name}`}
            columns={columns}
            rows={members}
            rowKey={(m) => m.user_id}
            empty={
              <EmptyState
                title="No members"
                description="Nobody is on this team — invite a teammate with the team name and its join password."
              />
            }
          />
        </Card>

        <Card title="Leave team">
          {leave.error && (
            <Alert tone="danger" title="Could not leave">
              {messageOf(leave.error)}
            </Alert>
          )}
          <p className="muted">
            {locked
              ? `This team has ${solves} ${solves === 1 ? "solve" : "solves"}. Its score is attributed to the players who earned it, so nobody can leave once a team has scored — ask an organiser if this is wrong.`
              : "You will lose your place on this team. You can create or join another afterwards."}
          </p>
          <Button
            variant="danger"
            disabled={locked}
            loading={leave.isPending}
            onClick={() => setConfirming(true)}
          >
            Leave {team.name}
          </Button>
        </Card>
      </div>

      <ConfirmDestructive
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() =>
          leave.mutate(undefined, {
            onSuccess: () => {
              setConfirming(false);
              toast.success("You left the team");
            },
            onError: () => setConfirming(false),
          })
        }
        resourceName={team.name}
        resourceKind="team"
        title="Leave team"
        description="You will be teamless until you join or create another team."
        confirmLabel="Leave team"
        busy={leave.isPending}
      />
    </>
  );
}

const PROFILE_FIELDS = [
  { key: "website", label: "Website" },
  { key: "affiliation", label: "Affiliation" },
  { key: "country", label: "Country" },
] as const;

/**
 * The team's public profile, with the unset fields called out.
 *
 * A team whose required fields are unfilled is refused play with `incomplete-team-profile`,
 * and the server sends no destination — this page is where a captain is meant to see what is
 * missing. It is also where the hole shows: there is no endpoint to edit a team, and the
 * custom registration fields an organiser can mark required are not readable at all, so the
 * honest thing is to name the gap rather than offer a form that posts nowhere.
 */
function TeamProfile({ team }: { team: Team }) {
  const missing = PROFILE_FIELDS.filter((f) => (team[f.key] ?? "") === "");

  return (
    <Card title="Team profile">
      {missing.length > 0 && (
        <Alert tone="warn" title="This profile is incomplete">
          {missing.map((f) => f.label).join(", ")} {missing.length === 1 ? "is" : "are"} not set. If
          the organisers marked a profile field required, the CTF will refuse to let this team play
          until it is filled — and flagfish has no API to edit a team yet, so ask an organiser to
          set it.
        </Alert>
      )}
      <dl className="ff-stack">
        {PROFILE_FIELDS.map((f) => {
          const value = team[f.key] ?? "";
          return (
            <div key={f.key} className="ff-row">
              <dt>{f.label}</dt>
              <dd>
                {value === "" ? <Badge tone="warn">not set</Badge> : value}
              </dd>
            </div>
          );
        })}
      </dl>
    </Card>
  );
}

/* --------------------------------------------------------------- error paths */

// The two denials this page answers with a different page, rather than a line under a field.
const STRUCTURAL: ReadonlySet<string> = new Set(["already-on-team", "team-creation-disabled"]);

/** The message a form shows for an error the page has no structural answer to. */
function formError(error: unknown): string | undefined {
  if (error === null || error === undefined) return undefined;
  const denial = denialOf(error);
  if (denial !== null && denial.location !== null) return undefined;
  if (denial?.reason != null && STRUCTURAL.has(denial.reason)) return undefined;
  return messageOf(error);
}

function messageOf(error: unknown): string {
  return isApiError(error) ? error.detail : "Something went wrong. Try again.";
}

/** Only a denial the server gave a destination for is PolicyGate's to follow. */
function redirecting(error: unknown): unknown {
  return denialOf(error)?.location != null ? error : null;
}

function LoadFailure({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  // Not a denial: a 500, a dropped connection. Say so and let the player retry — swallowing
  // it into "you have no team" would be a lie with a form attached.
  if (denialOf(error) === null) {
    return (
      <Alert tone="danger" title="Could not load your team">
        <p>{messageOf(error)}</p>
        <Button onClick={onRetry}>Retry</Button>
      </Alert>
    );
  }
  return <PolicyGate error={error} />;
}
