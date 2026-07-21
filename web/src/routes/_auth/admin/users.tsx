import { useEffect, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { AdminUser } from "../../../api/admin";
import { isApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import {
  adminUsersQuery,
  instanceQuery,
  useForcePasswordChange,
  useSetUserBanned,
  useSetUserHidden,
  useSetUserRole,
  useSetUserVerified,
  useUpdateUser,
  useVerifyAllUsers,
} from "../../../queries";
import {
  AwardsPanel,
  TriStateField,
  encodeText,
  triInvalid,
  triKeep,
  triPatch,
  type TriValue,
} from "../../../admin";
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
  useToast,
  type Column,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/users")({
  component: UsersPage,
});

// The two 409s this screen exists to explain. The server says the same thing; saying it
// again here means a conflict never degrades into "something went wrong".
const SELF_BAN = "you cannot ban yourself";
const LAST_ADMIN = "this is the last admin";

const SEARCH_FIELDS = [
  { value: "name", label: "name" },
  { value: "email", label: "email" },
  { value: "website", label: "website" },
  { value: "affiliation", label: "affiliation" },
  { value: "country", label: "country" },
] as const;

type SearchField = (typeof SEARCH_FIELDS)[number]["value"];

function UsersPage() {
  const toast = useToast();
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState(50);
  const [draft, setDraft] = useState("");
  const [field, setField] = useState<SearchField>("name");
  const [search, setSearch] = useState<{ q: string; field: SearchField }>({ q: "", field: "name" });

  const users = useQuery(
    adminUsersQuery({
      page,
      per_page: perPage,
      ...(search.q === "" ? {} : { q: search.q, field: search.field }),
    }),
  );

  const [banTarget, setBanTarget] = useState<AdminUser | null>(null);
  const [banConflict, setBanConflict] = useState<string | null>(null);
  const [roleTarget, setRoleTarget] = useState<AdminUser | null>(null);
  const [roleConflict, setRoleConflict] = useState<string | null>(null);
  const [editTarget, setEditTarget] = useState<AdminUser | null>(null);
  const [hideTarget, setHideTarget] = useState<AdminUser | null>(null);
  const [forceTarget, setForceTarget] = useState<AdminUser | null>(null);
  const [pointsTarget, setPointsTarget] = useState<AdminUser | null>(null);
  const [verifyTarget, setVerifyTarget] = useState<AdminUser | null>(null);
  const [verifyAllOpen, setVerifyAllOpen] = useState(false);

  // A manual award moves the scoring account. In users mode that is the user, so the per-user grant
  // belongs here; in teams mode the team is the scoring account and the control lives on the team.
  const instance = useQuery(instanceQuery);
  const usersMode = instance.data?.mode === "users";

  const setBanned = useSetUserBanned();
  const setRole = useSetUserRole();
  const setHidden = useSetUserHidden();
  const force = useForcePasswordChange();
  const setVerified = useSetUserVerified();
  const verifyAll = useVerifyAllUsers();

  const submitSearch = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setPage(1);
    setSearch({ q: draft.trim(), field });
  };

  const openBan = (user: AdminUser) => {
    setBanConflict(null);
    setBanTarget(user);
  };

  const openRole = (user: AdminUser) => {
    setRoleConflict(null);
    setRoleTarget(user);
  };

  const confirmBan = () => {
    if (banTarget === null) return;
    const next = !banTarget.banned;
    setBanned.mutate(
      { id: banTarget.id, banned: next },
      {
        onSuccess: (user) => {
          setBanTarget(null);
          toast.success(user.banned ? `${user.name} is banned` : `${user.name} is unbanned`);
        },
        onError: (error) => {
          if (isApiError(error) && error.status === 409) {
            setBanConflict(SELF_BAN);
            return;
          }
          toast.error("Could not change the ban", messageOf(error));
        },
      },
    );
  };

  const confirmRole = () => {
    if (roleTarget === null) return;
    const next = roleTarget.role === "admin" ? "user" : "admin";
    setRole.mutate(
      { id: roleTarget.id, role: next },
      {
        onSuccess: (user) => {
          setRoleTarget(null);
          toast.success(`${user.name} is now ${user.role === "admin" ? "an admin" : "a user"}`);
        },
        onError: (error) => {
          if (isApiError(error) && error.status === 409) {
            setRoleConflict(LAST_ADMIN);
            return;
          }
          toast.error("Could not change the role", messageOf(error));
        },
      },
    );
  };

  const columns: readonly Column<AdminUser>[] = [
    {
      key: "name",
      header: "name",
      cell: (u) => <span className="ff-truncate">{u.name}</span>,
    },
    {
      key: "email",
      header: "email",
      cell: (u) => <span className="ff-mono ff-truncate">{u.email}</span>,
    },
    {
      key: "verified",
      header: "verified",
      cell: (u) =>
        u.verified ? (
          <Badge tone="success">verified</Badge>
        ) : (
          <Badge tone="warn">unverified</Badge>
        ),
    },
    {
      key: "banned",
      header: "banned",
      cell: (u) => (u.banned ? <Badge tone="danger">banned</Badge> : <Dash />),
    },
    {
      key: "hidden",
      header: "hidden",
      cell: (u) => (u.hidden ? <Badge tone="neutral">hidden</Badge> : <Dash />),
    },
    {
      key: "role",
      header: "role",
      cell: (u) =>
        u.role === "admin" ? <Badge tone="accent">admin</Badge> : <Badge>user</Badge>,
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (u) => (
        <span className="ff-row">
          <Button size="sm" variant="ghost" onClick={() => setEditTarget(u)}>
            Edit
          </Button>
          {usersMode && (
            <Button size="sm" variant="ghost" onClick={() => setPointsTarget(u)}>
              Points
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => setHideTarget(u)}>
            {u.hidden ? "Unhide" : "Hide"}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setVerifyTarget(u)}>
            {u.verified ? "Un-verify" : "Mark verified"}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setForceTarget(u)}>
            Force new password
          </Button>
          <Button size="sm" variant="ghost" onClick={() => openRole(u)}>
            {u.role === "admin" ? "Demote" : "Make admin"}
          </Button>
          {/* A ban is never a bare button: the anticheat screens link straight into this
              table, and a cluster is evidence, not a verdict. */}
          <Button
            size="sm"
            variant={u.banned ? "secondary" : "danger"}
            onClick={() => openBan(u)}
          >
            {u.banned ? "Unban" : "Ban"}
          </Button>
        </span>
      ),
    },
  ];

  if (users.isError) {
    const denial = denialOf(users.error);
    if (denial !== null) return <PolicyGate error={users.error} />;
    return (
      <Alert tone="danger" title="Could not load users">
        <p>{messageOf(users.error)}</p>
        <Button size="sm" onClick={() => void users.refetch()}>
          Retry
        </Button>
      </Alert>
    );
  }

  const rows = users.data?.users ?? [];
  const banning = banTarget !== null && banTarget.banned;

  return (
    <>
      <div className="page-head">
        <h1>users</h1>
        <span className="muted">{users.data?.total ?? 0} accounts</span>
        <span className="ff-spacer" />
        {/* The recovery for a mailer that accepted everything and delivered nothing: without
            it every player sits unverified and 403ed, and there is no way back from here. */}
        <Button size="sm" variant="secondary" onClick={() => setVerifyAllOpen(true)}>
          Verify everyone
        </Button>
      </div>

      <Card>
        <form onSubmit={submitSearch} className="ff-row" role="search" aria-label="Search users">
          <Select
            aria-label="Search field"
            value={field}
            onChange={(e) => setField(e.target.value as SearchField)}
            options={SEARCH_FIELDS}
          />
          <Input
            aria-label="Search users"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="Search…"
            maxLength={256}
          />
          <Button type="submit" variant="secondary">
            Search
          </Button>
        </form>
      </Card>

      <Card flush>
        <DataTable
          caption="Users"
          columns={columns}
          rows={rows}
          rowKey={(u) => u.id}
          loading={users.isPending}
          empty={
            <EmptyState
              title="No users on this page"
              description="Registered players appear here as soon as they sign up."
            />
          }
          pagination={{
            page,
            perPage,
            total: users.data?.total,
            onPageChange: setPage,
            onPerPageChange: setPerPage,
          }}
        />
      </Card>

      {banTarget !== null && (
        <ConfirmDestructive
          open
          onClose={() => setBanTarget(null)}
          onConfirm={confirmBan}
          resourceName={banTarget.name}
          resourceKind="user"
          title={banning ? "Unban user" : "Ban user"}
          confirmLabel={banning ? "Unban" : "Ban"}
          busy={setBanned.isPending}
          description={
            banning
              ? "They get their session back and reappear on the scoreboard."
              : "They lose every session and API token immediately, and drop off the scoreboard."
          }
        >
          {banConflict !== null && (
            <Alert tone="danger" title="Refused">
              {banConflict}
            </Alert>
          )}
        </ConfirmDestructive>
      )}

      {editTarget !== null && (
        <EditUserDialog
          user={editTarget}
          onClose={() => setEditTarget(null)}
          onSaved={(name) => {
            setEditTarget(null);
            toast.success(`${name} saved`);
          }}
        />
      )}

      {hideTarget !== null && (
        <Dialog
          open
          size="sm"
          onClose={() => setHideTarget(null)}
          title={hideTarget.hidden ? "Unhide user" : "Hide user"}
          description={
            hideTarget.hidden
              ? `${hideTarget.name} reappears on the scoreboard and the public rosters.`
              : `${hideTarget.name} disappears from the scoreboard and the public rosters. They keep playing; their stamped solves stay put.`
          }
          footer={
            <>
              <Button variant="ghost" onClick={() => setHideTarget(null)}>
                Cancel
              </Button>
              <Button
                variant="primary"
                loading={setHidden.isPending}
                onClick={() => {
                  setHidden.mutate(
                    { id: hideTarget.id, hidden: !hideTarget.hidden },
                    {
                      onSuccess: (u) => {
                        setHideTarget(null);
                        toast.success(u.hidden ? `${u.name} is hidden` : `${u.name} is visible`);
                      },
                      onError: (error) =>
                        toast.error("Could not change visibility", messageOf(error)),
                    },
                  );
                }}
              >
                {hideTarget.hidden ? "Unhide" : "Hide"}
              </Button>
            </>
          }
        />
      )}

      {forceTarget !== null && (
        <ConfirmDestructive
          open
          onClose={() => setForceTarget(null)}
          onConfirm={() =>
            force.mutate(forceTarget.id, {
              onSuccess: (u) => {
                setForceTarget(null);
                toast.success(
                  `${u.name} must pick a new password`,
                  u.api_tokens_revoked > 0
                    ? `Their sessions are gone, along with ${u.api_tokens_revoked} API token${
                        u.api_tokens_revoked === 1 ? "" : "s"
                      }. Tell them: only they can mint a replacement.`
                    : "Their sessions are gone; they log back in and are walled until they change it.",
                );
              },
              onError: (error) =>
                toast.error("Could not force a password change", messageOf(error)),
            })
          }
          resourceName={forceTarget.name}
          resourceKind="user"
          title="Force a new password"
          confirmLabel="Force new password"
          busy={force.isPending}
          description="Use this when the credential is suspect. Every session AND every API token dies now; the account is walled everywhere except the password-change form until they comply. The wall does not stop a bearer token, which is why the tokens are deleted rather than blocked — warn the user, because only they can mint replacements."
        />
      )}

      {verifyTarget !== null && (
        <Dialog
          open
          size="sm"
          onClose={() => setVerifyTarget(null)}
          title={verifyTarget.verified ? "Un-verify user" : "Mark user verified"}
          description={
            verifyTarget.verified
              ? `${verifyTarget.name} is locked out of the challenges again until they confirm their address.`
              : `${verifyTarget.name} can play immediately, without a confirmation email. Use this when the mail never arrived.`
          }
          footer={
            <>
              <Button variant="ghost" onClick={() => setVerifyTarget(null)}>
                Cancel
              </Button>
              <Button
                variant={verifyTarget.verified ? "danger" : "primary"}
                loading={setVerified.isPending}
                onClick={() => {
                  setVerified.mutate(
                    { id: verifyTarget.id, verified: !verifyTarget.verified },
                    {
                      onSuccess: (u) => {
                        setVerifyTarget(null);
                        toast.success(
                          u.verified ? `${u.name} is verified` : `${u.name} is no longer verified`,
                        );
                      },
                      onError: (error) =>
                        toast.error("Could not change verification", messageOf(error)),
                    },
                  );
                }}
              >
                {verifyTarget.verified ? "Un-verify" : "Mark verified"}
              </Button>
            </>
          }
        />
      )}

      {verifyAllOpen && (
        <Dialog
          open
          size="sm"
          onClose={() => setVerifyAllOpen(false)}
          title="Verify everyone"
          description="Every unverified account is marked verified and can play at once. Accounts that are already verified are left alone. Each change is recorded against you in the audit log."
          footer={
            <>
              <Button variant="ghost" onClick={() => setVerifyAllOpen(false)}>
                Cancel
              </Button>
              <Button
                variant="primary"
                loading={verifyAll.isPending}
                onClick={() => {
                  verifyAll.mutate(undefined, {
                    onSuccess: (result) => {
                      setVerifyAllOpen(false);
                      toast.success(
                        result.verified === 0
                          ? "Nobody was waiting"
                          : `${result.verified} accounts verified`,
                        "They can play without a confirmation email.",
                      );
                    },
                    onError: (error) => toast.error("Could not verify everyone", messageOf(error)),
                  });
                }}
              >
                Verify everyone
              </Button>
            </>
          }
        />
      )}

      {pointsTarget !== null && (
        <Dialog
          open
          size="lg"
          onClose={() => setPointsTarget(null)}
          title={`Points — ${pointsTarget.name}`}
        >
          <AwardsPanel accountId={pointsTarget.id} accountKind="user" />
        </Dialog>
      )}

      {roleTarget !== null && (
        <Dialog
          open
          size="sm"
          onClose={() => setRoleTarget(null)}
          title={roleTarget.role === "admin" ? "Demote to user" : "Promote to admin"}
          description={
            roleTarget.role === "admin"
              ? `${roleTarget.name} loses the console and every admin operation.`
              : `${roleTarget.name} gains the console: config, challenges, flags, bans and the audit log.`
          }
          footer={
            <>
              <Button variant="ghost" onClick={() => setRoleTarget(null)}>
                Cancel
              </Button>
              <Button
                variant={roleTarget.role === "admin" ? "danger" : "primary"}
                loading={setRole.isPending}
                onClick={confirmRole}
              >
                {roleTarget.role === "admin" ? "Demote" : "Promote"}
              </Button>
            </>
          }
        >
          {roleConflict !== null && (
            <Alert tone="danger" title="Refused">
              {roleConflict} — promote someone else before demoting them.
            </Alert>
          )}
        </Dialog>
      )}
    </>
  );
}

/**
 * Name plus the tri-state profile fields. Everything else about a user — email, role, ban,
 * hide, team — moves through its own control, so this form cannot become a moderation tool.
 */
function EditUserDialog({
  user,
  onClose,
  onSaved,
}: {
  user: AdminUser;
  onClose: () => void;
  onSaved: (name: string) => void;
}) {
  const update = useUpdateUser();

  const [name, setName] = useState(user.name);
  const [website, setWebsite] = useState<TriValue>(triKeep(user.website ?? ""));
  const [affiliation, setAffiliation] = useState<TriValue>(triKeep(user.affiliation ?? ""));
  const [country, setCountry] = useState<TriValue>(triKeep(user.country ?? ""));

  useEffect(() => {
    setName(user.name);
    setWebsite(triKeep(user.website ?? ""));
    setAffiliation(triKeep(user.affiliation ?? ""));
    setCountry(triKeep(user.country ?? ""));
  }, [user]);

  const invalid =
    triInvalid(website, encodeText) ||
    triInvalid(affiliation, encodeText) ||
    triInvalid(country, encodeText);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (invalid) return;
    update.mutate(
      {
        id: user.id,
        body: {
          ...(name.trim() !== "" && name !== user.name ? { name: name.trim() } : {}),
          website: triPatch(website, encodeText),
          affiliation: triPatch(affiliation, encodeText),
          country: triPatch(country, encodeText),
        },
      },
      { onSuccess: (u) => onSaved(u.name) },
    );
  };

  const tri = (
    fieldName: string,
    label: string,
    value: TriValue,
    onChange: (v: TriValue) => void,
    current: string | null | undefined,
    clearNote: string,
  ) => (
    <TriStateField
      name={fieldName}
      label={label}
      value={value}
      onChange={onChange}
      current={current == null || current === "" ? "not set" : current}
      clearNote={clearNote}
    >
      {(control, v, onValue) => (
        <Input {...control} value={v} onChange={(e) => onValue(e.target.value)} maxLength={255} />
      )}
    </TriStateField>
  );

  return (
    <Dialog open onClose={onClose} title={`Edit ${user.name}`} size="lg">
      <Form
        onSubmit={submit}
        error={update.error ? messageOf(update.error) : undefined}
        footer={
          <>
            <Button variant="ghost" onClick={onClose} disabled={update.isPending}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={update.isPending} disabled={invalid}>
              Save
            </Button>
          </>
        }
      >
        <Field name="name" label="Name" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={128} required />
        </Field>
        {tri("website", "Website", website, setWebsite, user.website, "The profile will list no website.")}
        {tri(
          "affiliation",
          "Affiliation",
          affiliation,
          setAffiliation,
          user.affiliation,
          "The profile will list no affiliation.",
        )}
        {tri("country", "Country", country, setCountry, user.country, "The profile will list no country.")}
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
