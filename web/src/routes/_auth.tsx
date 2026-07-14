import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { api } from "../api/client";
import { meQuery } from "../queries";

export const Route = createFileRoute("/_auth")({
  beforeLoad: async ({ context, location }) => {
    try {
      const me = await context.queryClient.ensureQueryData(meQuery);
      return { me };
    } catch {
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
  },
  component: AuthLayout,
});

function AuthLayout() {
  const { me } = Route.useRouteContext();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const logout = useMutation({
    mutationFn: () => api.logout(),
    onSettled: async () => {
      queryClient.clear();
      await navigate({ to: "/login", search: { redirect: undefined } });
    },
  });

  return (
    <>
      <header className="topbar">
        <Link to="/challenges" className="brand">
          flagfish<span className="cursor">_</span>
        </Link>
        <nav>
          <Link to="/challenges">challenges</Link>
          <Link to="/scoreboard">scoreboard</Link>
          <Link to="/profile">profile</Link>
        </nav>
        <span className="spacer" />
        <span className="who">{me.name}</span>
        <button className="ghost" onClick={() => logout.mutate()} disabled={logout.isPending}>
          logout
        </button>
      </header>
      <Outlet />
    </>
  );
}
