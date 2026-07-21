import { useState, type FormEvent } from "react";
import { useQueries } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import { adminSubmissionsQuery } from "../../../queries";
import type { AdminSubmission, SubmissionType } from "../../../api/admin";
import { AdminPage, errorDetail } from "../../../admin";
import { denialOf, PolicyGate } from "../../../policy";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  Input,
  RelativeTime,
  Select,
  type BadgeTone,
  type Column,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/submissions")({
  component: SubmissionsPage,
});

const REFRESH_MS = 5_000;

const TYPE_TONE: Record<SubmissionType, BadgeTone> = {
  correct: "success",
  incorrect: "neutral",
  partial: "info",
  discard: "neutral",
  ratelimited: "warn",
};

const TYPE_OPTIONS = [
  { value: "", label: "any verdict" },
  { value: "correct", label: "correct" },
  { value: "incorrect", label: "incorrect" },
  { value: "partial", label: "partial" },
  { value: "discard", label: "discard" },
  { value: "ratelimited", label: "rate-limited" },
] as const;

const LIMIT_OPTIONS = [
  { value: "25", label: "25 per page" },
  { value: "50", label: "50 per page" },
  { value: "100", label: "100 per page" },
] as const;

/** The committed filter set. Absent keys are simply not sent, so the server applies its defaults. */
interface Applied {
  type?: SubmissionType;
  challenge_id?: number;
  user_id?: number;
  team_id?: number;
  limit?: number;
}

function intOrUndef(raw: string): number | undefined {
  if (raw === "") return undefined;
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? Math.trunc(n) : undefined;
}

function SubmissionsPage() {
  const [typeDraft, setTypeDraft] = useState("");
  const [challengeDraft, setChallengeDraft] = useState("");
  const [userDraft, setUserDraft] = useState("");
  const [teamDraft, setTeamDraft] = useState("");
  const [limitDraft, setLimitDraft] = useState("50");

  const [applied, setApplied] = useState<Applied>({ limit: 50 });
  const [live, setLive] = useState(true);

  // Keyset pagination: one query per loaded page, keyed by the opaque cursor that opens it. Only
  // the newest page polls, and only while it is the sole page — once an operator pages back, live
  // inserts would push rows across a now-stale boundary, so the poll stops until they return to it.
  const [cursors, setCursors] = useState<Array<string | undefined>>([undefined]);
  const polling = live && cursors.length === 1;

  const results = useQueries({
    queries: cursors.map((cursor, idx) => ({
      ...adminSubmissionsQuery({ ...applied, cursor }),
      refetchInterval: idx === 0 && polling ? REFRESH_MS : (false as const),
    })),
  });

  const firstError = results.find((r) => r.error)?.error ?? null;
  if (firstError !== null && denialOf(firstError) !== null) {
    return <PolicyGate error={firstError} />;
  }

  const apply = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setApplied({
      type: typeDraft === "" ? undefined : (typeDraft as SubmissionType),
      challenge_id: intOrUndef(challengeDraft),
      user_id: intOrUndef(userDraft),
      team_id: intOrUndef(teamDraft),
      limit: intOrUndef(limitDraft) ?? 50,
    });
    setCursors([undefined]);
  };

  const clear = () => {
    setTypeDraft("");
    setChallengeDraft("");
    setUserDraft("");
    setTeamDraft("");
    setLimitDraft("50");
    setApplied({ limit: 50 });
    setCursors([undefined]);
  };

  // Newest-first order is the server's; concatenating pages preserves it. Dedupe defensively so a
  // row that a live refresh lifted onto page one cannot also show under an older page boundary.
  const rows: AdminSubmission[] = [];
  const seen = new Set<number>();
  for (const r of results) {
    for (const s of r.data?.submissions ?? []) {
      if (!seen.has(s.id)) {
        seen.add(s.id);
        rows.push(s);
      }
    }
  }

  const lastData = results[results.length - 1]?.data;
  const nextCursor = lastData?.next_cursor ?? "";
  const loadMore = () => {
    if (nextCursor !== "") setCursors((cs) => [...cs, nextCursor]);
  };

  const loading = results.some((r) => r.isPending);

  const columns: Column<AdminSubmission>[] = [
    {
      key: "date",
      header: "time",
      width: "9rem",
      cell: (s) => <RelativeTime value={s.date} />,
    },
    {
      key: "type",
      header: "verdict",
      width: "7rem",
      cell: (s) => {
        const t = s.type as SubmissionType;
        return <Badge tone={TYPE_TONE[t] ?? "neutral"}>{s.type}</Badge>;
      },
    },
    {
      key: "who",
      header: "submitter",
      cell: (s) => (
        <span className="ff-row">
          <Link to="/users/$userId" params={{ userId: s.user_id }}>
            {s.user_name ?? `#${s.user_id}`}
          </Link>
          {s.team_id !== undefined && (
            <Link to="/teams/$teamId" params={{ teamId: s.team_id }} className="muted">
              {s.team_name ?? `team #${s.team_id}`}
            </Link>
          )}
        </span>
      ),
    },
    {
      key: "challenge",
      header: "challenge",
      cell: (s) => (
        <Link
          to="/admin/challenges/$challengeId"
          params={{ challengeId: String(s.challenge_id) }}
          className="ff-truncate"
        >
          {s.challenge_name}
        </Link>
      ),
    },
    {
      key: "provided",
      header: "attempt",
      cell: (s) => <span className="ff-mono ff-truncate">{s.provided}</span>,
    },
    {
      key: "attributed",
      header: "attributed",
      width: "7rem",
      align: "right",
      cell: (s) =>
        s.attributed_account_id === undefined ? (
          <span className="muted">—</span>
        ) : (
          <span className="ff-mono muted">#{s.attributed_account_id}</span>
        ),
    },
    {
      key: "ip",
      header: "address",
      width: "10rem",
      cell: (s) => (s.ip === undefined ? <span className="muted">—</span> : <span className="ff-mono">{s.ip}</span>),
    },
  ];

  return (
    <AdminPage
      title="Submissions"
      description="Every flag attempt as it lands — correct and not. The newest page refreshes itself; pause it to read a row in peace."
      actions={
        <Button
          size="sm"
          variant={live ? "primary" : "ghost"}
          onClick={() => setLive((v) => !v)}
          aria-pressed={live}
        >
          {live ? "Live · pause" : "Paused · resume"}
        </Button>
      }
    >
      <Card title="Filters">
        <form className="ff-row" style={{ flexWrap: "wrap", alignItems: "flex-end" }} onSubmit={apply}>
          <label className="ff-field">
            <span className="muted">Verdict</span>
            <Select value={typeDraft} onChange={(e) => setTypeDraft(e.target.value)} options={TYPE_OPTIONS} />
          </label>
          <label className="ff-field">
            <span className="muted">Challenge id</span>
            <Input
              inputMode="numeric"
              placeholder="any"
              value={challengeDraft}
              onChange={(e) => setChallengeDraft(e.target.value.replace(/\D/g, ""))}
              style={{ width: "7rem" }}
            />
          </label>
          <label className="ff-field">
            <span className="muted">User id</span>
            <Input
              inputMode="numeric"
              placeholder="any"
              value={userDraft}
              onChange={(e) => setUserDraft(e.target.value.replace(/\D/g, ""))}
              style={{ width: "7rem" }}
            />
          </label>
          <label className="ff-field">
            <span className="muted">Team id</span>
            <Input
              inputMode="numeric"
              placeholder="any"
              value={teamDraft}
              onChange={(e) => setTeamDraft(e.target.value.replace(/\D/g, ""))}
              style={{ width: "7rem" }}
            />
          </label>
          <label className="ff-field">
            <span className="muted">Page size</span>
            <Select value={limitDraft} onChange={(e) => setLimitDraft(e.target.value)} options={LIMIT_OPTIONS} />
          </label>
          <Button type="submit" size="sm" variant="primary">
            Apply
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={clear}>
            Clear
          </Button>
        </form>
      </Card>

      {firstError !== null && (
        <Alert tone="danger" title="The log did not answer">
          {errorDetail(firstError)}
        </Alert>
      )}

      <Card
        flush
        footer={
          nextCursor !== "" ? (
            <Button size="sm" onClick={loadMore}>
              Load older
            </Button>
          ) : undefined
        }
      >
        <DataTable
          caption="Flag submissions, newest first"
          columns={columns}
          rows={rows}
          rowKey={(s) => s.id}
          loading={loading}
          dense
          empty={
            <EmptyState
              title="No submissions match"
              description="Nothing has been submitted under these filters yet. Loosen them, or wait — the newest page refreshes on its own."
            />
          }
        />
      </Card>
    </AdminPage>
  );
}
