import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, createFileRoute, useChildMatches } from "@tanstack/react-router";
import type { AdminTeam } from "../../../api/admin";
import { isApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import {
  adminTeamsQuery,
  useCreateAdminTeam,
  useSetTeamBanned,
  useSetTeamHidden,
} from "../../../queries";
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDestructive,
  DataTable,
  Dialog,
  EmptyState,
  Field,
  Form,
  Input,
  Select,
  fieldErrors,
  useToast,
  type Column,
  type FieldErrors,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/teams")({
  component: TeamsRoute,
});

// The detail route nests under this one; when it matches, this screen steps aside.
function TeamsRoute() {
  const children = useChildMatches();
  return children.length > 0 ? <Outlet /> : <TeamsPage />;
}

const SEARCH_FIELDS = [
  { value: "name", label: "name" },
  { value: "email", label: "email" },
  { value: "website", label: "website" },
  { value: "affiliation", label: "affiliation" },
  { value: "country", label: "country" },
] as const;

type SearchField = (typeof SEARCH_FIELDS)[number]["value"];

// The 409 this screen exists to explain: a team ban may never orphan the console.
const SELF_TEAM_BAN = "you cannot ban your own team";

function TeamsPage() {
  const toast = useToast();
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState(50);
  // The draft is what the operator types; the submitted pair is what the list queries.
  // Splitting them means keystrokes do not fire requests and Enter means "search".
  const [draft, setDraft] = useState("");
  const [field, setField] = useState<SearchField>("name");
  const [search, setSearch] = useState<{ q: string; field: SearchField }>({ q: "", field: "name" });

  const params = {
    page,
    per_page: perPage,
    ...(search.q === "" ? {} : { q: search.q, field: search.field }),
  };
  const teams = useQuery(adminTeamsQuery(params));

  const [creating, setCreating] = useState(false);
  const [banTarget, setBanTarget] = useState<AdminTeam | null>(null);
  const [banConflict, setBanConflict] = useState<string | null>(null);
  const [hideTarget, setHideTarget] = useState<AdminTeam | null>(null);

  const setBanned = useSetTeamBanned();
  const setHidden = useSetTeamHidden();

  const submitSearch = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setPage(1);
    setSearch({ q: draft.trim(), field });
  };

  const confirmBan = () => {
    if (banTarget === null) return;
    setBanned.mutate(
      { id: banTarget.id, banned: !banTarget.banned },
      {
        onSuccess: (team) => {
          setBanTarget(null);
          toast.success(team.banned ? `${team.name} is banned` : `${team.name} is unbanned`);
        },
        onError: (error) => {
          if (isApiError(error) && error.status === 409) {
            setBanConflict(SELF_TEAM_BAN);
            return;
          }
          toast.error("Could not change the ban", messageOf(error));
        },
      },
    );
  };

  const confirmHide = () => {
    if (hideTarget === null) return;
    setHidden.mutate(
      { id: hideTarget.id, hidden: !hideTarget.hidden },
      {
        onSuccess: (team) => {
          setHideTarget(null);
          toast.success(team.hidden ? `${team.name} is hidden` : `${team.name} is visible`);
        },
        onError: (error) => toast.error("Could not change visibility", messageOf(error)),
      },
    );
  };

  const columns: readonly Column<AdminTeam>[] = [
    {
      key: "name",
      header: "name",
      cell: (t) => (
        <Link to="/admin/teams/$teamId" params={{ teamId: String(t.id) }} className="ff-truncate">
          {t.name}
        </Link>
      ),
    },
    {
      key: "email",
      header: "email",
      cell: (t) => (t.email == null ? <Dash /> : <span className="ff-mono ff-truncate">{t.email}</span>),
    },
    {
      key: "country",
      header: "country",
      width: "7rem",
      cell: (t) => t.country ?? <Dash />,
    },
    {
      key: "members",
      header: "members",
      align: "right",
      width: "6rem",
      cell: (t) => t.member_count,
    },
    {
      key: "flags",
      header: "state",
      cell: (t) => (
        <span className="ff-row">
          {t.banned && <Badge tone="danger">banned</Badge>}
          {t.hidden && <Badge tone="neutral">hidden</Badge>}
          {!t.banned && !t.hidden && <Dash />}
        </span>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (t) => (
        <span className="ff-row">
          <Button size="sm" variant="ghost" onClick={() => setHideTarget(t)}>
            {t.hidden ? "Unhide" : "Hide"}
          </Button>
          <Button
            size="sm"
            variant={t.banned ? "secondary" : "danger"}
            onClick={() => {
              setBanConflict(null);
              setBanTarget(t);
            }}
          >
            {t.banned ? "Unban" : "Ban"}
          </Button>
        </span>
      ),
    },
  ];

  if (teams.isError) {
    const denial = denialOf(teams.error);
    if (denial !== null) return <PolicyGate error={teams.error} />;
    return (
      <Alert tone="danger" title="Could not load teams">
        <p>{messageOf(teams.error)}</p>
        <Button size="sm" onClick={() => void teams.refetch()}>
          Retry
        </Button>
      </Alert>
    );
  }

  const rows = teams.data?.teams ?? [];
  const banning = banTarget !== null && !banTarget.banned;

  return (
    <>
      <div className="page-head">
        <h1>teams</h1>
        <span className="muted">{teams.data?.total ?? 0} teams</span>
      </div>

      <div className="ff-stack">
        <Card>
          <form onSubmit={submitSearch} className="ff-row" role="search" aria-label="Search teams">
            <Select
              aria-label="Search field"
              value={field}
              onChange={(e) => setField(e.target.value as SearchField)}
              options={SEARCH_FIELDS}
            />
            <Input
              aria-label="Search teams"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Search…"
              maxLength={256}
            />
            <Button type="submit" variant="secondary">
              Search
            </Button>
            <span style={{ marginLeft: "auto" }}>
              <Button variant="primary" onClick={() => setCreating(true)}>
                New team
              </Button>
            </span>
          </form>
        </Card>

        <Card flush>
          <DataTable
            caption="Teams"
            columns={columns}
            rows={rows}
            rowKey={(t) => t.id}
            loading={teams.isPending}
            empty={
              <EmptyState
                title={search.q === "" ? "No teams yet" : "No teams match"}
                description={
                  search.q === ""
                    ? "Teams appear here when players create them — or create one yourself."
                    : "Try a different needle or another field."
                }
              />
            }
            pagination={{
              page,
              perPage,
              total: teams.data?.total,
              onPageChange: setPage,
              onPerPageChange: setPerPage,
            }}
          />
        </Card>
      </div>

      <CreateTeamDialog open={creating} onClose={() => setCreating(false)} />

      {banTarget !== null && (
        <ConfirmDestructive
          open
          onClose={() => setBanTarget(null)}
          onConfirm={confirmBan}
          resourceName={banTarget.name}
          resourceKind="team"
          title={banning ? "Ban team" : "Unban team"}
          confirmLabel={banning ? "Ban team" : "Unban team"}
          busy={setBanned.isPending}
          description={
            banning
              ? "Every member loses their sessions immediately, keeps losing API access on every credential, and the team drops off the scoreboard."
              : "Members can log in again and the team reappears on the scoreboard."
          }
        >
          {banConflict !== null && (
            <Alert tone="danger" title="Refused">
              {banConflict} — an admin must never ban the instance out from under itself.
            </Alert>
          )}
        </ConfirmDestructive>
      )}

      {hideTarget !== null && (
        <Dialog
          open
          size="sm"
          onClose={() => setHideTarget(null)}
          title={hideTarget.hidden ? "Unhide team" : "Hide team"}
          description={
            hideTarget.hidden
              ? `${hideTarget.name} reappears on the scoreboard and its public page comes back.`
              : `${hideTarget.name} disappears from the scoreboard and its public page 404s. Members keep playing; their stamped solves stay put.`
          }
          footer={
            <>
              <Button variant="ghost" onClick={() => setHideTarget(null)}>
                Cancel
              </Button>
              <Button variant="primary" loading={setHidden.isPending} onClick={confirmHide}>
                {hideTarget.hidden ? "Unhide" : "Hide"}
              </Button>
            </>
          }
        />
      )}
    </>
  );
}

function CreateTeamDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const create = useCreateAdminTeam();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [email, setEmail] = useState("");
  const [country, setCountry] = useState("");

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    create.mutate(
      {
        name,
        password: password === "" ? undefined : password,
        email: email === "" ? undefined : email,
        country: country === "" ? undefined : country,
      },
      {
        onSuccess: (team) => {
          setName("");
          setPassword("");
          setEmail("");
          setCountry("");
          onClose();
          toast.success("Team created", `${team.name} is captainless until its first member joins.`);
        },
      },
    );
  };

  return (
    <Dialog open={open} onClose={onClose} title="New team" size="sm">
      <Form
        onSubmit={submit}
        error={create.error ? messageOf(create.error) : undefined}
        errors={fieldsOf(create.error)}
        footer={
          <>
            <Button variant="ghost" onClick={onClose} disabled={create.isPending}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={create.isPending}>
              Create team
            </Button>
          </>
        }
      >
        <Field name="name" label="Team name" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={128} required />
        </Field>
        <Field
          name="password"
          label="Join password"
          hint="Players need it to join. Blank lets anyone in."
        >
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            maxLength={128}
            autoComplete="new-password"
          />
        </Field>
        <Field name="email" label="Contact email" hint="Visible to admins and the team only.">
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} maxLength={255} />
        </Field>
        <Field name="country" label="Country">
          <Input value={country} onChange={(e) => setCountry(e.target.value)} maxLength={64} />
        </Field>
      </Form>
    </Dialog>
  );
}

function Dash() {
  return (
    <span className="muted" aria-label="no">
      —
    </span>
  );
}

function messageOf(error: unknown): string {
  if (isApiError(error)) return error.detail;
  return error instanceof Error ? error.message : "unexpected error";
}

function fieldsOf(error: unknown): FieldErrors | undefined {
  if (!isApiError(error) || error.fieldErrors.length === 0) return undefined;
  return fieldErrors({ errors: error.fieldErrors });
}
