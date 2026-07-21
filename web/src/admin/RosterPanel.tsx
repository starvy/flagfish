import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { AdminTeamMember } from "../api/admin";
import { denialOf, PolicyGate } from "../policy";
import {
  adminTeamMembersQuery,
  adminTeamsQuery,
  useMoveTeamMember,
  useRemoveTeamMember,
} from "../queries";
import {
  Badge,
  Button,
  Card,
  DataTable,
  Dialog,
  EmptyState,
  Field,
  Input,
  Select,
  Skeleton,
  useToast,
  type Column,
  type SelectOption,
} from "../ui";
import { errorDetail } from "./errors";

/**
 * Mid-event roster repair: take a player off this team, or hand them to another one. Both are the
 * organizer's answer to a player who registered into the wrong team, and both leave the scoreboard
 * alone — solves and awards carry the team they were scored for, so only future points follow the
 * player. Banning the whole team was the only tool before this, and it punishes everyone.
 */
export function RosterPanel({ teamId, teamName }: { teamId: number; teamName: string }) {
  const roster = useQuery(adminTeamMembersQuery(teamId));
  const [removing, setRemoving] = useState<AdminTeamMember | null>(null);
  const [moving, setMoving] = useState<AdminTeamMember | null>(null);

  const columns: readonly Column<AdminTeamMember>[] = [
    {
      key: "name",
      header: "Player",
      cell: (m) => (
        <span className="ff-row">
          <span className="ff-truncate">{m.name}</span>
          {m.captain && <Badge tone="accent">captain</Badge>}
          {m.banned && <Badge tone="danger">banned</Badge>}
          {m.hidden && <Badge tone="warn">hidden</Badge>}
        </span>
      ),
    },
    {
      key: "email",
      header: "Email",
      cell: (m) => <span className="ff-truncate muted">{m.email}</span>,
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      width: "18rem",
      cell: (m) => (
        <span className="ff-row" style={{ justifyContent: "flex-end", gap: "0.5rem" }}>
          <Button size="sm" variant="secondary" onClick={() => setMoving(m)}>
            Move to team…
          </Button>
          <Button size="sm" variant="danger" onClick={() => setRemoving(m)}>
            Remove
          </Button>
        </span>
      ),
    },
  ];

  if (roster.isError) {
    const denial = denialOf(roster.error);
    if (denial !== null) return <PolicyGate error={roster.error} />;
    return (
      <Card title="Roster">
        <p className="muted">Could not load the roster: {errorDetail(roster.error)}</p>
        <Button size="sm" onClick={() => void roster.refetch()}>
          Retry
        </Button>
      </Card>
    );
  }

  return (
    <Card title="Roster" flush>
      {roster.isPending ? (
        <div style={{ padding: "var(--space-4, 1rem)" }}>
          <Skeleton lines={3} />
        </div>
      ) : (
        <DataTable
          caption={`Members of ${teamName}`}
          columns={columns}
          rows={roster.data.members ?? []}
          rowKey={(m) => m.user_id}
          empty={
            <EmptyState
              title="No members"
              description="Nobody is on this team. Players join with the team name and its join password."
            />
          }
        />
      )}

      {removing !== null && (
        <RemoveDialog
          teamId={teamId}
          teamName={teamName}
          member={removing}
          onClose={() => setRemoving(null)}
        />
      )}
      {moving !== null && (
        <MoveDialog
          teamId={teamId}
          teamName={teamName}
          member={moving}
          onClose={() => setMoving(null)}
        />
      )}
    </Card>
  );
}

function RemoveDialog({
  teamId,
  teamName,
  member,
  onClose,
}: {
  teamId: number;
  teamName: string;
  member: AdminTeamMember;
  onClose: () => void;
}) {
  const toast = useToast();
  const remove = useRemoveTeamMember(teamId);

  return (
    <Dialog
      open
      size="sm"
      onClose={onClose}
      title="Remove from team"
      description={`${member.name} leaves ${teamName} and becomes teamless. Points they already scored stay with ${teamName} and the scoreboard does not move.${
        member.captain ? " They hold the captain's seat, which passes to a remaining member." : ""
      }`}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={remove.isPending}>
            Cancel
          </Button>
          <Button
            variant="danger"
            loading={remove.isPending}
            onClick={() =>
              remove.mutate(member.user_id, {
                onSuccess: () => {
                  onClose();
                  toast.success(`${member.name} was removed from ${teamName}`);
                },
                onError: (error) => toast.error("Could not remove the member", errorDetail(error)),
              })
            }
          >
            Remove
          </Button>
        </>
      }
    />
  );
}

function MoveDialog({
  teamId,
  teamName,
  member,
  onClose,
}: {
  teamId: number;
  teamName: string;
  member: AdminTeamMember;
  onClose: () => void;
}) {
  const toast = useToast();
  const move = useMoveTeamMember(teamId);
  const [search, setSearch] = useState("");
  const [target, setTarget] = useState("");

  // Search rather than one long list: an instance can hold far more teams than a picker can show.
  const teams = useQuery(adminTeamsQuery({ per_page: 50, ...(search === "" ? {} : { q: search }) }));

  const options: readonly SelectOption[] = (teams.data?.teams ?? [])
    .filter((t) => t.id !== teamId)
    .map((t) => ({ value: String(t.id), label: t.name }));

  const submit = () => {
    const toTeamId = Number(target);
    if (!Number.isInteger(toTeamId) || toTeamId <= 0) return;
    const destination = options.find((o) => o.value === target)?.label ?? "the new team";
    move.mutate(
      { userId: member.user_id, toTeamId },
      {
        onSuccess: () => {
          onClose();
          toast.success(`${member.name} moved to ${destination}`);
        },
        onError: (error) => toast.error("Could not move the member", errorDetail(error)),
      },
    );
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title="Move to another team"
      description={`${member.name} joins the team you pick and leaves ${teamName}. Points they scored for ${teamName} stay there — a move changes who they score for next, not the standings.${
        member.captain ? " They hold the captain's seat here, which passes to a remaining member." : ""
      }`}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={move.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={move.isPending}
            disabled={target === ""}
            onClick={submit}
          >
            Move
          </Button>
        </>
      }
    >
      <Field name="team-search" label="Find a team" hint="Filters the list below by name.">
        <Input
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setTarget("");
          }}
          placeholder="team name"
        />
      </Field>
      <Field
        name="team-target"
        label="Destination team"
        hint="A team at its size cap refuses the move."
        required
      >
        <Select
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          options={options}
          placeholder={teams.isPending ? "loading teams…" : "select a team"}
          disabled={teams.isPending}
        />
      </Field>
      {!teams.isPending && options.length === 0 && (
        <p className="muted">No other team matches that search.</p>
      )}
    </Dialog>
  );
}
