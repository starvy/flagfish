import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, createFileRoute, notFound } from "@tanstack/react-router";
import { formErrorOf } from "../../admin";
import { instanceQuery, meQuery, useUpdateConfig } from "../../queries";
import { Button, Dialog, ToastProvider, useToast } from "../../ui";
import "../../admin/admin.css";

export const Route = createFileRoute("/_auth/admin")({
  // Not a 403 page: for a non-admin the console is not a place they may not go, it is a place
  // that does not exist. The guard is where the route lives, so no child can forget it.
  beforeLoad: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(meQuery);
    if (!me.is_admin) throw notFound();
    return { me };
  },
  component: AdminConsole,
});

// Overview matches exactly; every other entry is a prefix, so a challenge editor keeps
// Challenges lit.
const NAV = [
  { to: "/admin", label: "Overview", exact: true },
  { to: "/admin/config", label: "Config", exact: false },
  { to: "/admin/challenges", label: "Challenges", exact: false },
  { to: "/admin/pool", label: "Pool", exact: false },
  { to: "/admin/pages", label: "Pages", exact: false },
  { to: "/admin/tags", label: "Tags", exact: false },
  { to: "/admin/users", label: "Users", exact: false },
  { to: "/admin/teams", label: "Teams", exact: false },
  { to: "/admin/brackets", label: "Brackets", exact: false },
  { to: "/admin/fields", label: "Fields", exact: false },
  { to: "/admin/notifications", label: "Notifications", exact: false },
  { to: "/admin/audit", label: "Audit", exact: false },
  { to: "/admin/anticheat", label: "Anticheat", exact: false },
] as const;

function AdminConsole() {
  const { me } = Route.useRouteContext();

  return (
    <ToastProvider>
      <div className="admin">
        <aside className="admin__sidebar">
          <Link to="/admin" className="admin__brand">
            <span className="admin__brand-name">flagfish</span>
            <span className="admin__brand-tag">console</span>
          </Link>

          <nav className="admin__nav" aria-label="Console">
            {NAV.map((item) => (
              <Link
                key={item.to}
                to={item.to}
                className="admin__nav-link"
                activeOptions={{ exact: item.exact }}
              >
                {item.label}
              </Link>
            ))}
          </nav>

          <div className="admin__sidebar-foot">
            <span className="ff-truncate">signed in as {me.name}</span>
            <Link to="/challenges" className="admin__nav-link">
              ← back to the game
            </Link>
          </div>
        </aside>

        <main className="admin__main">
          <PauseControl />
          <Outlet />
        </main>
      </div>
    </ToastProvider>
  );
}

/**
 * The one-click incident switch. Pausing refuses every flag submission fleet-wide — admins
 * included — so it lives in the shell, not three screens deep in the config form, and it never
 * fires without a confirmation. The paused state is read from the public instance snapshot,
 * which the config mutation invalidates: every admin tab shows the banner, not just the one
 * that clicked.
 */
function PauseControl() {
  const instance = useQuery(instanceQuery);
  const update = useUpdateConfig();
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);

  // The committed public schema does not describe `paused` yet; read it off the raw body the
  // same way the player shell does.
  const paused = Boolean((instance.data as { paused?: boolean } | undefined)?.paused);

  if (!instance.data) return null;

  const flip = (next: boolean) => {
    update.mutate(
      { paused: next },
      {
        onSuccess: () => {
          setConfirming(false);
          toast.success(
            next ? "Event paused" : "Event resumed",
            next
              ? "Every flag submission is refused until you resume."
              : "Submissions count again.",
          );
        },
        onError: (error) => {
          setConfirming(false);
          toast.error(next ? "Could not pause" : "Could not resume", formErrorOf(error));
        },
      },
    );
  };

  return (
    <>
      {paused ? (
        <div className="admin__pausebar admin__pausebar--paused" role="status">
          <span>
            <strong>The event is paused.</strong> Every flag submission is refused — for admins
            too.
          </span>
          <Button size="sm" variant="primary" onClick={() => setConfirming(true)}>
            Resume
          </Button>
        </div>
      ) : (
        <div className="admin__pausebar">
          <span>Incident? Pausing refuses every flag submission, fleet-wide, until resumed.</span>
          <Button size="sm" variant="danger" onClick={() => setConfirming(true)}>
            Pause event
          </Button>
        </div>
      )}
      <Dialog
        open={confirming}
        onClose={() => setConfirming(false)}
        size="sm"
        title={paused ? "Resume the event?" : "Pause the event?"}
        description={
          paused
            ? "Submissions are accepted again the moment you confirm."
            : "Every flag submission — every player, every admin — is refused until you resume. Browsing and hint unlocks keep working. The pause is public: every client shows it."
        }
        footer={
          <>
            <Button variant="ghost" onClick={() => setConfirming(false)} disabled={update.isPending}>
              Cancel
            </Button>
            <Button
              variant={paused ? "primary" : "danger"}
              loading={update.isPending}
              onClick={() => flip(!paused)}
            >
              {paused ? "Resume the event" : "Pause the event"}
            </Button>
          </>
        }
      />
    </>
  );
}
