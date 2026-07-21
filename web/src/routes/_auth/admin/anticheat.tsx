import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  accountReportQuery,
  flagSharingQuery,
  ipOverlapQuery,
  unissuedSolvesQuery,
} from "../../../queries";
import { ApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import { useInstanceState } from "../../../shell/instance";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  Input,
  RelativeTime,
  Tabs,
  type Column,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/anticheat")({
  component: AnticheatPage,
});

interface SharingPair {
  issued_to: number;
  issued_to_name: string;
  submitter: number;
  submitter_name: string;
  submission_count: number;
  challenge_count: number;
  challenge_ids: number[];
  first_seen: string;
  last_seen: string;
}

interface IPCluster {
  ip: string;
  account_count: number;
  account_ids: number[];
  // Index-aligned with account_ids: account_names[i] names account_ids[i].
  account_names: string[];
  first_seen: string;
  last_seen: string;
}

interface SharingEdge {
  direction: string;
  counterparty: number;
  counterparty_name: string;
  submission_count: number;
  challenge_ids: number[];
  first_seen: string;
  last_seen: string;
}

interface IPEdge {
  ip: string;
  other_account_id: number;
  other_account_name: string;
  submission_count: number;
  first_seen: string;
  last_seen: string;
}

function AnticheatPage() {
  const [tab, setTab] = useState("sharing");
  const [account, setAccount] = useState<number | null>(null);

  const inspect = (id: number) => {
    setAccount(id);
    setTab("account");
  };

  return (
    <div className="ff-stack">
      <div className="page-head">
        <h1>anticheat</h1>
        <span className="muted">signals, not verdicts</span>
      </div>

      <Alert tone="info" title="This page is evidence, not a judgement">
        A detector reports a coincidence: two accounts submitted the same issued flag, or several
        accounts were seen from one address. Neither fact proves cheating. A university, a
        conference wifi, a phone tether and a shared VPN exit all put honest players behind one IP,
        and a flag can leak without its owner knowing. Read a row as a reason to look, not as a
        result. Nothing here bans anybody — that decision lives on the{" "}
        <Link to="/admin/users">users screen</Link>, behind a confirmation, where a human takes it.
      </Alert>

      <Tabs
        label="Anticheat detectors"
        value={tab}
        onChange={setTab}
        items={[
          { id: "sharing", label: "Flag sharing", content: <SharingTab onInspect={inspect} /> },
          { id: "overlap", label: "IP overlap", content: <OverlapTab onInspect={inspect} /> },
          { id: "unissued", label: "Unissued solves", content: <UnissuedTab onInspect={inspect} /> },
          {
            id: "account",
            label: "Account",
            // Keyed on the account so the id box follows a drill-down from another tab.
            content: <AccountTab key={account ?? "none"} account={account} onAccount={setAccount} />,
          },
        ]}
      />
    </div>
  );
}

function SharingTab({ onInspect }: { onInspect: (id: number) => void }) {
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState(50);

  const q = useQuery(flagSharingQuery({ page, per_page: perPage }));
  const denial = q.error === null ? null : denialOf(q.error);
  if (denial !== null) return <PolicyGate error={q.error} />;

  const columns: Column<SharingPair>[] = [
    {
      key: "issued_to",
      header: "issued to",
      width: "12rem",
      cell: (p) => <AccountRef id={p.issued_to} name={p.issued_to_name} onInspect={onInspect} />,
    },
    {
      key: "submitter",
      header: "submitted by",
      width: "12rem",
      cell: (p) => <AccountRef id={p.submitter} name={p.submitter_name} onInspect={onInspect} />,
    },
    {
      key: "submissions",
      header: "submissions",
      align: "right",
      width: "7rem",
      cell: (p) => p.submission_count,
    },
    {
      key: "challenges",
      header: "challenges",
      cell: (p) => (
        <span className="ff-row">
          <Badge tone="info">{p.challenge_count}</Badge>
          <span className="ff-mono muted ff-truncate">#{p.challenge_ids.join(", #")}</span>
        </span>
      ),
    },
    {
      key: "first",
      header: "first seen",
      width: "9rem",
      cell: (p) => <RelativeTime value={p.first_seen} />,
    },
    {
      key: "last",
      header: "last seen",
      width: "9rem",
      cell: (p) => <RelativeTime value={p.last_seen} />,
    },
    {
      key: "act",
      header: "Review",
      headerHidden: true,
      align: "right",
      width: "6rem",
      cell: () => <Link to="/admin/users">review</Link>,
    },
  ];

  return (
    <Card title="A flag issued to one account, submitted by another" flush>
      {q.isError ? (
        <ErrorPanel error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <DataTable
          caption="Flag-sharing pairs"
          columns={columns}
          rows={(q.data?.pairs ?? []) as unknown as SharingPair[]}
          rowKey={(p) => `${p.issued_to}:${p.submitter}`}
          loading={q.isPending}
          dense
          empty={
            <EmptyState
              title="No shared flags"
              description="A pair appears when a unique flag issued to one account is submitted by another. On static-flag challenges this detector has nothing to say."
            />
          }
          pagination={{
            page,
            perPage,
            total: q.data?.total ?? 0,
            onPageChange: setPage,
            onPerPageChange: setPerPage,
          }}
        />
      )}
    </Card>
  );
}

interface UnissuedSolve {
  account_id: number;
  user_id: number;
  user_name: string;
  team_id?: number;
  team_name?: string;
  challenge_id: number;
  challenge_name: string;
  date: string;
}

function UnissuedTab({ onInspect }: { onInspect: (id: number) => void }) {
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState(50);
  const { teamsMode } = useInstanceState();

  const q = useQuery(unissuedSolvesQuery({ page, per_page: perPage }));
  const denial = q.error === null ? null : denialOf(q.error);
  if (denial !== null) return <PolicyGate error={q.error} />;

  const columns: Column<UnissuedSolve>[] = [
    {
      key: "account",
      header: teamsMode ? "team / who" : "account",
      width: "14rem",
      // In teams mode the account is the team, but a solve is one human's act: the AccountRef points
      // at the scoring team, and the submitting user rides underneath so an admin knows who to talk to.
      cell: (s) => (
        <span className="ff-stack" style={{ gap: "0.15rem" }}>
          <AccountRef
            id={s.account_id}
            name={teamsMode ? s.team_name : s.user_name}
            onInspect={onInspect}
          />
          {teamsMode && s.user_name !== "" && (
            <span className="muted" style={{ paddingLeft: "0.6rem" }}>
              by {s.user_name}
            </span>
          )}
        </span>
      ),
    },
    {
      key: "challenge",
      header: "challenge",
      cell: (s) => (
        <span className="ff-row">
          <span className="ff-mono muted">#{s.challenge_id}</span>
          <span className="ff-truncate">{s.challenge_name}</span>
        </span>
      ),
    },
    {
      key: "when",
      header: "solved",
      width: "9rem",
      cell: (s) => <RelativeTime value={s.date} />,
    },
    {
      key: "act",
      header: "Review",
      headerHidden: true,
      align: "right",
      width: "6rem",
      cell: () => <Link to="/admin/users">review</Link>,
    },
  ];

  return (
    <Card title="A unique-flag challenge solved by an account never issued an instance" flush>
      {q.isError ? (
        <ErrorPanel error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <DataTable
          caption="Solves with no issued instance"
          columns={columns}
          rows={(q.data?.solves ?? []) as unknown as UnissuedSolve[]}
          rowKey={(s) => `${s.account_id}:${s.challenge_id}`}
          loading={q.isPending}
          dense
          empty={
            <EmptyState
              title="Nothing unaccounted for"
              description="Every solve on a unique-flag challenge maps to an instance issued to that account. A row here would mean a flag was accepted that this account was never handed — worth a very close look."
            />
          }
          pagination={{
            page,
            perPage,
            total: q.data?.total ?? 0,
            onPageChange: setPage,
            onPerPageChange: setPerPage,
          }}
        />
      )}
    </Card>
  );
}

function OverlapTab({ onInspect }: { onInspect: (id: number) => void }) {
  const [draft, setDraft] = useState("2");
  const [minAccounts, setMinAccounts] = useState(2);
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState(50);

  const q = useQuery(ipOverlapQuery({ min_accounts: minAccounts, page, per_page: perPage }));
  const denial = q.error === null ? null : denialOf(q.error);
  if (denial !== null) return <PolicyGate error={q.error} />;

  const apply = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const n = Number(draft);
    if (!Number.isFinite(n)) return;
    setMinAccounts(Math.min(1000, Math.max(2, Math.trunc(n))));
    setPage(1);
  };

  const columns: Column<IPCluster>[] = [
    { key: "ip", header: "address", width: "14rem", cell: (c) => <span className="ff-mono">{c.ip}</span> },
    {
      key: "count",
      header: "accounts",
      align: "right",
      width: "6rem",
      cell: (c) => <Badge tone="info">{c.account_count}</Badge>,
    },
    {
      key: "ids",
      header: "who",
      cell: (c) => (
        <span className="ff-row" style={{ flexWrap: "wrap" }}>
          {c.account_ids.map((id, idx) => (
            <AccountRef key={id} id={id} name={c.account_names?.[idx]} onInspect={onInspect} />
          ))}
        </span>
      ),
    },
    {
      key: "first",
      header: "first seen",
      width: "9rem",
      cell: (c) => <RelativeTime value={c.first_seen} />,
    },
    {
      key: "last",
      header: "last seen",
      width: "9rem",
      cell: (c) => <RelativeTime value={c.last_seen} />,
    },
    {
      key: "act",
      header: "Review",
      headerHidden: true,
      align: "right",
      width: "6rem",
      cell: () => <Link to="/admin/users">review</Link>,
    },
  ];

  return (
    <div className="ff-stack">
      <Card title="Accounts seen from one address">
        <form className="ff-row" onSubmit={apply}>
          <label className="muted" htmlFor="min-accounts">
            cluster at
          </label>
          <Input
            id="min-accounts"
            inputMode="numeric"
            value={draft}
            onChange={(e) => setDraft(e.target.value.replace(/\D/g, ""))}
            style={{ width: "6rem" }}
          />
          <span className="muted">accounts or more (2–1000)</span>
          <Button type="submit" size="sm" variant="primary">
            Apply
          </Button>
        </form>
        <p className="muted">
          Raise the threshold to cut through shared NAT: a campus egress with fifty players on it is
          normal, two accounts on a home address at 3am is worth a look. The number is a lens, not a
          line between innocent and guilty.
        </p>
      </Card>

      <Card flush>
        {q.isError ? (
          <ErrorPanel error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <DataTable
            caption="IP-overlap clusters"
            columns={columns}
            rows={(q.data?.clusters ?? []) as unknown as IPCluster[]}
            rowKey={(c) => c.ip}
            loading={q.isPending}
            dense
            empty={
              <EmptyState
                title="No address is shared by that many accounts"
                description={`Nothing reaches ${minAccounts} accounts on one address. Lower the threshold to widen the net.`}
              />
            }
            pagination={{
              page,
              perPage,
              total: q.data?.total ?? 0,
              onPageChange: setPage,
              onPerPageChange: setPerPage,
            }}
          />
        )}
      </Card>
    </div>
  );
}

function AccountTab({
  account,
  onAccount,
}: {
  account: number | null;
  onAccount: (id: number | null) => void;
}) {
  const [draft, setDraft] = useState(account === null ? "" : String(account));

  const q = useQuery({ ...accountReportQuery(account ?? 0), enabled: account !== null });
  const denial = q.error === null ? null : denialOf(q.error);
  if (denial !== null) return <PolicyGate error={q.error} />;

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    onAccount(draft === "" ? null : Number(draft));
  };

  const sharing = (q.data?.sharing ?? []) as unknown as SharingEdge[];
  const overlap = (q.data?.ip_overlap ?? []) as unknown as IPEdge[];
  const accountName = ((q.data as { account_name?: string } | undefined)?.account_name ?? "").trim();
  // Name first when we have one, id in tow — a header an operator can read at a glance.
  const subject =
    account === null ? "" : accountName !== "" ? `${accountName} · #${account}` : `#${account}`;

  const sharingColumns: Column<SharingEdge>[] = [
    {
      key: "direction",
      header: "direction",
      width: "10rem",
      cell: (e) => <Badge tone="info">{e.direction}</Badge>,
    },
    {
      key: "counterparty",
      header: "counterparty",
      width: "12rem",
      cell: (e) => (
        <AccountRef id={e.counterparty} name={e.counterparty_name} onInspect={(id) => onAccount(id)} />
      ),
    },
    {
      key: "subs",
      header: "submissions",
      align: "right",
      width: "7rem",
      cell: (e) => e.submission_count,
    },
    {
      key: "challenges",
      header: "challenges",
      cell: (e) => <span className="ff-mono muted">#{e.challenge_ids.join(", #")}</span>,
    },
    {
      key: "first",
      header: "first seen",
      width: "9rem",
      cell: (e) => <RelativeTime value={e.first_seen} />,
    },
    {
      key: "last",
      header: "last seen",
      width: "9rem",
      cell: (e) => <RelativeTime value={e.last_seen} />,
    },
  ];

  const overlapColumns: Column<IPEdge>[] = [
    { key: "ip", header: "address", width: "14rem", cell: (e) => <span className="ff-mono">{e.ip}</span> },
    {
      key: "other",
      header: "shared with",
      width: "12rem",
      cell: (e) => (
        <AccountRef
          id={e.other_account_id}
          name={e.other_account_name}
          onInspect={(id) => onAccount(id)}
        />
      ),
    },
    {
      key: "subs",
      header: "submissions",
      align: "right",
      width: "7rem",
      cell: (e) => e.submission_count,
    },
    {
      key: "first",
      header: "first seen",
      width: "9rem",
      cell: (e) => <RelativeTime value={e.first_seen} />,
    },
    {
      key: "last",
      header: "last seen",
      width: "9rem",
      cell: (e) => <RelativeTime value={e.last_seen} />,
    },
  ];

  return (
    <div className="ff-stack">
      <Card title="Everything both detectors have on one account">
        <form className="ff-row" onSubmit={submit}>
          <Input
            inputMode="numeric"
            placeholder="account id"
            value={draft}
            onChange={(e) => setDraft(e.target.value.replace(/\D/g, ""))}
            style={{ width: "10rem" }}
            aria-label="Account id"
          />
          <Button type="submit" size="sm" variant="primary" disabled={draft === ""}>
            Inspect
          </Button>
          {account !== null && <Link to="/admin/users">review this account on the users screen</Link>}
        </form>
      </Card>

      {account === null ? (
        <EmptyState
          title="Pick an account"
          description="Type an account id, or use a row on the flag-sharing or IP-overlap tab. The report gathers every edge the detectors have for it."
        />
      ) : q.isError ? (
        <ErrorPanel error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <>
          <Card title={`Shared flags — account ${subject}`} flush>
            <DataTable
              caption={`Flag-sharing edges for account ${account}`}
              columns={sharingColumns}
              rows={sharing}
              rowKey={(e, i) => `${e.direction}:${e.counterparty}:${i}`}
              loading={q.isPending}
              dense
              empty={
                <EmptyState
                  title="No shared flags"
                  description="No unique flag issued to this account was submitted elsewhere, and it submitted none of anyone else's."
                />
              }
            />
          </Card>

          <Card title={`Shared addresses — account ${subject}`} flush>
            <DataTable
              caption={`IP-overlap edges for account ${account}`}
              columns={overlapColumns}
              rows={overlap}
              rowKey={(e, i) => `${e.ip}:${e.other_account_id}:${i}`}
              loading={q.isPending}
              dense
              empty={
                <EmptyState
                  title="No shared addresses"
                  description="Every address this account submitted from was its own."
                />
              }
            />
          </Card>
        </>
      )}
    </div>
  );
}

// AccountRef shows the account's display name with the id kept as a subtitle: this is the one screen
// where an operator is triaging an accusation and most needs to read *who*, but the id stays visible
// because it is the stable key an admin may still need to quote. An empty name — a deleted account
// the detectors still have evidence for — falls back to the bare id.
function AccountRef({
  id,
  name,
  onInspect,
}: {
  id: number;
  name?: string;
  onInspect: (id: number) => void;
}) {
  const named = name !== undefined && name !== "";
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => onInspect(id)}
      title={named ? `${name} · account #${id}` : `account #${id}`}
    >
      {named ? (
        <span className="ff-row" style={{ gap: "0.4rem" }}>
          <span className="ff-truncate">{name}</span>
          <span className="ff-mono muted">#{id}</span>
        </span>
      ) : (
        <span className="ff-mono">#{id}</span>
      )}
    </Button>
  );
}

function ErrorPanel({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  return (
    <div className="ff-card__body">
      <Alert tone="danger" title="The detector did not answer">
        <p>{error instanceof ApiError ? error.detail : "Something went wrong."}</p>
        <Button size="sm" onClick={onRetry}>
          Retry
        </Button>
      </Alert>
    </div>
  );
}
