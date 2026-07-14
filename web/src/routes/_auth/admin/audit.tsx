import { useMemo, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { adminAuditQuery } from "../../../queries";
import type { AuditParams } from "../../../api/admin";
import { ApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  Dialog,
  EmptyState,
  Input,
  RelativeTime,
  Select,
  type BadgeTone,
  type Column,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/audit")({
  component: AuditPage,
});

interface Entry {
  id: number;
  actor_id?: number | null;
  action: string;
  target_table: string;
  target_id?: number | null;
  before?: unknown;
  after?: unknown;
  at: string;
  ip?: string | null;
}

interface Filters {
  actor: string;
  action: "" | "INSERT" | "UPDATE" | "DELETE";
  target_table: string;
  target_id: string;
}

const NO_FILTERS: Filters = { actor: "", action: "", target_table: "", target_id: "" };

const ACTION_TONE: Record<string, BadgeTone> = {
  INSERT: "success",
  UPDATE: "info",
  DELETE: "warn",
};

function AuditPage() {
  const [filters, setFilters] = useState<Filters>(NO_FILTERS);
  const [applied, setApplied] = useState<Filters>(NO_FILTERS);
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState(50);
  const [open, setOpen] = useState<Entry | null>(null);

  const params = useMemo<AuditParams>(
    () => ({ page, per_page: perPage, ...toParams(applied) }),
    [page, perPage, applied],
  );
  const audit = useQuery(adminAuditQuery(params));

  const apply = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setApplied(filters);
    setPage(1);
  };

  const clear = () => {
    setFilters(NO_FILTERS);
    setApplied(NO_FILTERS);
    setPage(1);
  };

  const denial = audit.error === null ? null : denialOf(audit.error);
  if (denial !== null) return <PolicyGate error={audit.error} />;

  const columns: Column<Entry>[] = [
    {
      key: "at",
      header: "when",
      width: "9rem",
      cell: (e) => <RelativeTime value={e.at} />,
    },
    {
      key: "actor",
      header: "actor",
      width: "8rem",
      cell: (e) =>
        e.actor_id == null ? (
          <span className="muted">system</span>
        ) : (
          <span className="ff-mono">#{e.actor_id}</span>
        ),
    },
    {
      key: "action",
      header: "action",
      width: "7rem",
      cell: (e) => <Badge tone={ACTION_TONE[e.action] ?? "neutral"}>{e.action}</Badge>,
    },
    {
      key: "target",
      header: "target",
      cell: (e) => (
        <span className="ff-mono">
          {e.target_table}
          {e.target_id == null ? "" : ` #${e.target_id}`}
        </span>
      ),
    },
    {
      key: "ip",
      header: "ip",
      width: "10rem",
      cell: (e) => <span className="ff-mono muted">{e.ip ?? "—"}</span>,
    },
    {
      key: "diff",
      header: "Inspect",
      headerHidden: true,
      align: "right",
      width: "7rem",
      cell: (e) => (
        <Button size="sm" variant="ghost" onClick={() => setOpen(e)}>
          diff
        </Button>
      ),
    },
  ];

  const rows = (audit.data?.entries ?? []) as unknown as Entry[];
  const total = audit.data?.total ?? 0;

  return (
    <div className="ff-stack">
      <div className="page-head">
        <h1>audit</h1>
        <span className="muted">every write the database saw, newest first</span>
      </div>

      <Alert tone="danger" title="These rows can contain flag plaintext">
        The trail captures the row as it was written, so a challenge or flag edit carries the flag
        itself, and a user edit carries an email. Treat what is on this page as secret: do not
        screenshot it, do not paste it into a chat, and do not open it where the room can see. The
        payload stays hidden until you ask for it.
      </Alert>

      <Card title="Filters">
        <form className="ff-row" onSubmit={apply} style={{ flexWrap: "wrap" }}>
          <Input
            aria-label="Actor user id"
            placeholder="actor id"
            inputMode="numeric"
            value={filters.actor}
            onChange={(e) => setFilters({ ...filters, actor: digits(e.target.value) })}
            style={{ width: "8rem" }}
          />
          <Select
            aria-label="Action"
            value={filters.action}
            onChange={(e) => setFilters({ ...filters, action: e.target.value as Filters["action"] })}
            options={[
              { value: "", label: "any action" },
              { value: "INSERT", label: "INSERT" },
              { value: "UPDATE", label: "UPDATE" },
              { value: "DELETE", label: "DELETE" },
            ]}
          />
          <Input
            aria-label="Target table"
            placeholder="target table"
            value={filters.target_table}
            onChange={(e) => setFilters({ ...filters, target_table: e.target.value })}
            style={{ width: "12rem" }}
          />
          <Input
            aria-label="Target id"
            placeholder="target id"
            inputMode="numeric"
            value={filters.target_id}
            onChange={(e) => setFilters({ ...filters, target_id: digits(e.target.value) })}
            style={{ width: "8rem" }}
          />
          <Button type="submit" variant="primary" size="sm">
            Apply
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={clear}>
            Clear
          </Button>
        </form>
      </Card>

      {audit.isError ? (
        <Alert tone="danger" title="Could not load the audit trail">
          <p>{messageOf(audit.error)}</p>
          <Button size="sm" onClick={() => void audit.refetch()}>
            Retry
          </Button>
        </Alert>
      ) : (
        <DataTable
          caption="Audit trail"
          columns={columns}
          rows={rows}
          rowKey={(e) => e.id}
          loading={audit.isPending}
          dense
          stickyHeader
          empty={
            <EmptyState
              title="No entries match"
              description={
                isFiltered(applied)
                  ? "Loosen the filters — the trail is written by triggers, so a real write is never missing."
                  : "Nothing has been written yet. Every insert, update and delete lands here."
              }
            />
          }
          pagination={{
            page,
            perPage,
            total,
            onPageChange: setPage,
            onPerPageChange: setPerPage,
          }}
        />
      )}

      <DiffDialog entry={open} onClose={() => setOpen(null)} />
    </div>
  );
}

function DiffDialog({ entry, onClose }: { entry: Entry | null; onClose: () => void }) {
  const [revealed, setRevealed] = useState(false);

  const lines = useMemo(
    () => (entry === null ? [] : diff(jsonLines(entry.before), jsonLines(entry.after))),
    [entry],
  );

  const close = () => {
    setRevealed(false);
    onClose();
  };

  return (
    <Dialog
      open={entry !== null}
      onClose={close}
      size="lg"
      title={
        entry === null
          ? ""
          : `${entry.action} ${entry.target_table}${entry.target_id == null ? "" : ` #${entry.target_id}`}`
      }
      description="Red is the row before the write, green is the row after."
      footer={
        <Button variant="ghost" onClick={close}>
          Close
        </Button>
      }
    >
      {!revealed ? (
        <div className="ff-stack">
          <Alert tone="warn" title="Hidden on purpose">
            This payload may contain a flag in plaintext. Reveal it only when nobody is looking at
            your screen and nothing is recording it.
          </Alert>
          <Button variant="danger" onClick={() => setRevealed(true)}>
            Reveal payload
          </Button>
        </div>
      ) : lines.length === 0 ? (
        <p className="muted">This entry captured no row body.</p>
      ) : (
        // Selection is off deliberately: the whole point of the warning above is that this text
        // should not travel by an idle ⌘C into a chat window.
        <pre
          className="ff-code__pre ff-mono"
          style={{ maxHeight: "26rem", userSelect: "none" }}
          tabIndex={0}
          aria-label="Before and after diff"
        >
          <code>
            {lines.map((l, i) => (
              <span
                key={i}
                style={{ display: "block" }}
                className={
                  l.kind === "add" ? "ff-diff__add" : l.kind === "del" ? "ff-diff__del" : undefined
                }
              >
                {SIGIL[l.kind]}
                {l.text}
              </span>
            ))}
          </code>
        </pre>
      )}
    </Dialog>
  );
}

type LineKind = "same" | "add" | "del";

const SIGIL: Record<LineKind, string> = { same: "  ", add: "+ ", del: "- " };

interface DiffLine {
  kind: LineKind;
  text: string;
}

function jsonLines(value: unknown): string[] {
  if (value === null || value === undefined) return [];
  return JSON.stringify(value, null, 2).split("\n");
}

/**
 * A real line diff, not two blobs side by side: an LCS walk over the pretty-printed rows, so a
 * one-field UPDATE shows one changed line and the operator can see at a glance what moved.
 */
function diff(before: readonly string[], after: readonly string[]): DiffLine[] {
  const n = before.length;
  const m = after.length;
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));

  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] =
        before[i] === after[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }

  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (before[i] === after[j]) {
      out.push({ kind: "same", text: before[i] });
      i++;
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ kind: "del", text: before[i++] });
    } else {
      out.push({ kind: "add", text: after[j++] });
    }
  }
  while (i < n) out.push({ kind: "del", text: before[i++] });
  while (j < m) out.push({ kind: "add", text: after[j++] });
  return out;
}

function digits(s: string): string {
  return s.replace(/\D/g, "");
}

function isFiltered(f: Filters): boolean {
  return f.actor !== "" || f.action !== "" || f.target_table !== "" || f.target_id !== "";
}

// The API treats 0 and "" as unset, so an empty box means "do not filter", never "match zero".
function toParams(f: Filters): Omit<AuditParams, "page" | "per_page"> {
  return {
    ...(f.actor === "" ? {} : { actor: Number(f.actor) }),
    ...(f.action === "" ? {} : { action: f.action }),
    ...(f.target_table === "" ? {} : { target_table: f.target_table }),
    ...(f.target_id === "" ? {} : { target_id: Number(f.target_id) }),
  };
}

function messageOf(e: unknown): string {
  return e instanceof ApiError ? e.detail : "Something went wrong.";
}
