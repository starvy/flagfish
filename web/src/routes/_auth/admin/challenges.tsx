import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, createFileRoute, useChildMatches } from "@tanstack/react-router";
import { isApiError } from "../../../api/client";
import type { AdminChallengeListItem } from "../../../api/admin";
import {
  adminChallengesQuery,
  useDeleteChallenge,
  useReorderChallenges,
  useSetChallengeState,
} from "../../../queries";
import { PolicyGate, denialOf } from "../../../policy";
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDestructive,
  DataTable,
  EmptyState,
  useToast,
  type Column,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/challenges")({
  component: ChallengesRoute,
});

// The editor nests under this route in the file tree, but it is a page of its own, not a panel
// inside the board. When a child matches, the board steps out of its way.
function ChallengesRoute() {
  const children = useChildMatches();
  return children.length > 0 ? <Outlet /> : <AdminChallengesPage />;
}

/**
 * The board as an operator sees it: every challenge, hidden ones included, each row carrying the
 * state and the flag/hint counts a player is never told.
 */
type AdminRow = AdminChallengeListItem;

function AdminChallengesPage() {
  const board = useQuery(adminChallengesQuery);
  const toast = useToast();

  const reorder = useReorderChallenges();
  const setState = useSetChallengeState();
  const remove = useDeleteChallenge();

  // The reorder is edited locally and committed as one PUT; N drags must not be N requests.
  const [draft, setDraft] = useState<AdminRow[] | null>(null);
  const [dragging, setDragging] = useState<number | null>(null);

  const [target, setTarget] = useState<AdminRow | null>(null);
  const [refused, setRefused] = useState<string | null>(null);

  const rows = draft ?? board.data?.challenges ?? [];

  const moveTo = (from: number, to: number) => {
    if (to < 0 || to >= rows.length || from === to) return;
    const next = [...rows];
    const [row] = next.splice(from, 1);
    next.splice(to, 0, row!);
    setDraft(next);
  };

  const commitOrder = async () => {
    try {
      const out = await reorder.mutateAsync(rows.map((r, i) => ({ id: r.id, position: i })));
      setDraft(null);
      toast.success("Order saved", `${out.reordered} challenges repositioned.`);
    } catch (e) {
      toast.error("Could not save the order", messageOf(e));
    }
  };

  const toggleState = async (row: AdminRow) => {
    const next = stateOf(row) === "visible" ? "hidden" : "visible";
    try {
      const saved = await setState.mutateAsync({ id: row.id, state: next });
      toast.success(saved.state === "visible" ? "Published" : "Hidden", saved.name);
    } catch (e) {
      toast.error("Could not change the state", messageOf(e));
    }
  };

  const confirmDelete = async () => {
    if (!target) return;
    try {
      await remove.mutateAsync(target.id);
      toast.success("Challenge deleted", target.name);
      setTarget(null);
      setRefused(null);
    } catch (e) {
      // A challenge with solves is a 409, and the server's sentence is the one worth reading:
      // the solve ledger is scoreboard history, so the only way out is to hide it.
      if (isApiError(e) && e.status === 409) {
        setRefused(e.detail);
        return;
      }
      toast.error("Could not delete the challenge", messageOf(e));
    }
  };

  const hideInstead = async () => {
    if (!target) return;
    try {
      await setState.mutateAsync({ id: target.id, state: "hidden" });
      toast.success("Hidden", `${target.name} is no longer on the board.`);
      setTarget(null);
      setRefused(null);
    } catch (e) {
      toast.error("Could not hide the challenge", messageOf(e));
    }
  };

  const columns: readonly Column<AdminRow>[] = [
    {
      key: "order",
      header: "Order",
      width: "8rem",
      cell: (row, i) => (
        <div
          className="ff-row"
          draggable
          onDragStart={() => setDragging(i)}
          onDragEnd={() => setDragging(null)}
          onDragOver={(e) => {
            if (dragging !== null) e.preventDefault();
          }}
          onDrop={(e) => {
            e.preventDefault();
            if (dragging !== null) moveTo(dragging, i);
            setDragging(null);
          }}
        >
          <span className="ff-muted" aria-hidden="true" title="Drag to reorder">
            ⠿
          </span>
          {/* Drag is not an input everyone has: the same move is one keypress away. */}
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Move ${row.name} up`}
            disabled={i === 0}
            onClick={() => moveTo(i, i - 1)}
          >
            ↑
          </Button>
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Move ${row.name} down`}
            disabled={i === rows.length - 1}
            onClick={() => moveTo(i, i + 1)}
          >
            ↓
          </Button>
        </div>
      ),
    },
    {
      key: "name",
      header: "Name",
      cell: (row) => (
        <Link to="/admin/challenges/$challengeId" params={{ challengeId: String(row.id) }}>
          {row.name}
        </Link>
      ),
    },
    { key: "category", header: "Category", cell: (row) => row.category },
    { key: "value", header: "Value", align: "right", cell: (row) => row.value },
    {
      key: "function",
      header: "Scoring",
      cell: (row) => <span className="ff-muted">{row.function}</span>,
    },
    { key: "flags", header: "Flags", align: "right", cell: (row) => row.flag_count },
    { key: "hints", header: "Hints", align: "right", cell: (row) => row.hint_count },
    { key: "solves", header: "Solves", align: "right", cell: (row) => row.solve_count },
    {
      key: "state",
      header: "State",
      cell: (row) =>
        stateOf(row) === "visible" ? (
          <Badge tone="success">published</Badge>
        ) : (
          <Badge tone="neutral">hidden</Badge>
        ),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (row) => (
        <div className="ff-row">
          <Button size="sm" onClick={() => void toggleState(row)} disabled={setState.isPending}>
            {stateOf(row) === "visible" ? "Hide" : "Publish"}
          </Button>
          <Button
            size="sm"
            variant="danger"
            onClick={() => {
              setRefused(null);
              setTarget(row);
            }}
          >
            Delete
          </Button>
        </div>
      ),
    },
  ];

  if (board.isError) {
    return denialOf(board.error) ? (
      <PolicyGate error={board.error} />
    ) : (
      <Alert tone="danger" title="Could not load the challenges">
        <p>{messageOf(board.error)}</p>
        <Button onClick={() => void board.refetch()}>Retry</Button>
      </Alert>
    );
  }

  return (
    <div className="ff-stack">
      <div className="page-head">
        <h1>Challenges</h1>
        <Link to="/admin/challenges/$challengeId" params={{ challengeId: "new" }}>
          <Button variant="primary">New challenge</Button>
        </Link>
      </div>

      {draft !== null && (
        <Alert tone="info" title="The order has changed">
          <div className="ff-row">
            <span>Nothing moves for players until it is saved.</span>
            <Button variant="primary" loading={reorder.isPending} onClick={() => void commitOrder()}>
              Save order
            </Button>
            <Button variant="ghost" disabled={reorder.isPending} onClick={() => setDraft(null)}>
              Discard
            </Button>
          </div>
        </Alert>
      )}

      <Card flush>
        <DataTable
          caption="Challenges, in board order"
          columns={columns}
          rows={rows}
          rowKey={(row) => row.id}
          loading={board.isPending}
          empty={
            <EmptyState
              title="No challenges yet"
              description="A challenge needs a name, a category, a value and at least one flag before anyone can solve it."
              action={
                <Link to="/admin/challenges/$challengeId" params={{ challengeId: "new" }}>
                  <Button variant="primary">New challenge</Button>
                </Link>
              }
            />
          }
        />
      </Card>

      <ConfirmDestructive
        open={target !== null}
        onClose={() => {
          setTarget(null);
          setRefused(null);
        }}
        onConfirm={() => void confirmDelete()}
        resourceKind="challenge"
        resourceName={target?.name ?? ""}
        description="Its flags, hints and files go with it."
        busy={remove.isPending}
      >
        {refused !== null && (
          <Alert tone="danger" title="This challenge cannot be deleted">
            <p>{refused}</p>
            <Button
              variant="secondary"
              loading={setState.isPending}
              onClick={() => void hideInstead()}
            >
              Hide it instead
            </Button>
          </Alert>
        )}
      </ConfirmDestructive>
    </div>
  );
}

function stateOf(row: AdminRow): "visible" | "hidden" {
  return row.state === "hidden" ? "hidden" : "visible";
}

function messageOf(e: unknown): string {
  return isApiError(e) ? e.detail : e instanceof Error ? e.message : String(e);
}
