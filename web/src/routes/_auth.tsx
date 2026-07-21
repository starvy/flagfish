import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, createFileRoute, redirect } from "@tanstack/react-router";
import { NotificationBell, NotificationsDrawer } from "../notifications";
import { denialOf } from "../policy";
import { instanceQuery, meQuery, pagesQuery } from "../queries";
import { ClockBanners, UserMenu, useInstanceState } from "../shell";
import { applyLocalePreference } from "../ui";

export const Route = createFileRoute("/_auth")({
  beforeLoad: async ({ context, location }) => {
    // The shell's whole shape — team nav, countdown, freeze, pause — comes from the instance,
    // so it is warmed here rather than popping in a frame after the page it decorates.
    void context.queryClient.prefetchQuery(instanceQuery);
    try {
      return { me: await context.queryClient.ensureQueryData(meQuery) };
    } catch (error) {
      // A denial that names somewhere to go is the server routing us, not a dead session.
      // The forced-password-change wall blocks `/me` itself, so answering every failure
      // with /login would bounce the user between the two forever and never show them the
      // one form that lifts the wall.
      const denial = denialOf(error);
      if (denial !== null && denial.location !== null && denial.location !== "/login") {
        throw redirect({ href: denial.location });
      }
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
  },
  component: AuthLayout,
});

function AuthLayout() {
  const { me } = Route.useRouteContext();
  const { ctfName, teamsMode } = useInstanceState();

  // Published content pages become nav links. Drafts never reach this list — the server filters
  // them — so a page appears here the moment it is published and disappears when unpublished.
  const pages = useQuery(pagesQuery);

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
            {(pages.data?.pages ?? []).map((p) => (
              <Link key={p.route} to="/pages/$route" params={{ route: p.route }}>
                {p.title}
              </Link>
            ))}
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
