import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { AdminUser } from "../../../api/admin";
import { isApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import { adminUsersQuery, useSetUserBanned, useSetUserRole } from "../../../queries";
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDestructive,
  DataTable,
  Dialog,
  EmptyState,
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

function UsersPage() {
  const toast = useToast();
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState(50);

  const users = useQuery(adminUsersQuery({ page, per_page: perPage }));

  const [banTarget, setBanTarget] = useState<AdminUser | null>(null);
  const [banConflict, setBanConflict] = useState<string | null>(null);
  const [roleTarget, setRoleTarget] = useState<AdminUser | null>(null);
  const [roleConflict, setRoleConflict] = useState<string | null>(null);

  const setBanned = useSetUserBanned();
  const setRole = useSetUserRole();

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
      </div>

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
