import { Link, Outlet, createFileRoute, notFound } from "@tanstack/react-router";
import { meQuery } from "../../queries";
import { ToastProvider } from "../../ui";
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
  { to: "/admin/tags", label: "Tags", exact: false },
  { to: "/admin/users", label: "Users", exact: false },
  { to: "/admin/brackets", label: "Brackets", exact: false },
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
          <Outlet />
        </main>
      </div>
    </ToastProvider>
  );
}
