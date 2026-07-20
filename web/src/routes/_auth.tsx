import { useEffect } from "react";
import { Link, Outlet, createFileRoute, redirect } from "@tanstack/react-router";
import { NotificationBell, NotificationsDrawer } from "../notifications";
import { instanceQuery, meQuery } from "../queries";
import { ClockBanners, UserMenu, useInstanceState } from "../shell";
import { applyLocalePreference } from "../ui";

export const Route = createFileRoute("/_auth")({
  beforeLoad: async ({ context, location }) => {
    // The shell's whole shape — team nav, countdown, freeze, pause — comes from the instance,
    // so it is warmed here rather than popping in a frame after the page it decorates.
    void context.queryClient.prefetchQuery(instanceQuery);
    try {
      return { me: await context.queryClient.ensureQueryData(meQuery) };
    } catch {
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
  },
  component: AuthLayout,
});

function AuthLayout() {
  const { me } = Route.useRouteContext();
  const { ctfName, teamsMode } = useInstanceState();

  // The preference becomes real here: every locale-sensitive formatter downstream resolves
  // against it, and the document lang follows the account rather than the browser.
  useEffect(() => {
    applyLocalePreference(me.language);
  }, [me.language]);

  return (
    <div className="sh-app">
      <a className="sh-skip" href="#main">
        skip to content
      </a>

      <header className="sh-header">
        <div className="sh-header__inner">
          <Link to="/challenges" className="brand sh-brand">
            {ctfName}
            <span className="cursor">_</span>
          </Link>

          <nav className="sh-nav" aria-label="Primary">
            <Link to="/challenges">challenges</Link>
            <Link to="/scoreboard">scoreboard</Link>
            <Link to="/notifications" search={{ page: 1 }}>
              notifications
            </Link>
            {/* In users mode this route does not exist for anyone — the server answers 404. */}
            {teamsMode && <Link to="/team">team</Link>}
            <Link to="/settings" search={{ tab: "profile" }}>
              settings
            </Link>
            {me.is_admin && (
              <Link to="/admin" className="sh-nav__admin">
                admin
              </Link>
            )}
          </nav>

          <span className="ff-spacer" />

          <NotificationBell />
          <UserMenu me={me} />
        </div>
      </header>

      <ClockBanners />

      <main className="sh-main" id="main">
        <Outlet />
      </main>

      {/* Mounted unconditionally: it is the session's single subscriber to the event stream. */}
      <NotificationsDrawer />
    </div>
  );
}
