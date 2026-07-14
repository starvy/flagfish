import { useEffect } from "react";
import { QueryClientProvider, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { Outlet, createRootRouteWithContext, useRouter } from "@tanstack/react-router";
import { setUnauthorizedHandler } from "../api/client";
import { qk } from "../queries";
import { AnnouncerProvider, NotFoundScreen, RouteError } from "../shell";
import { ThemeProvider } from "../theme/provider";
import { ToastProvider } from "../ui";
import "../shell/shell.css";

export interface RouterContext {
  queryClient: QueryClient;
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: RootLayout,
  errorComponent: RouteError,
  notFoundComponent: NotFoundScreen,
});

/**
 * A 401 anywhere means the session died under us — expired, revoked, or logged out in another
 * tab. The cached identity is the one thing that must not survive it, or the auth guard would
 * wave the next navigation through on a session that no longer exists.
 */
function useSessionExpiry() {
  const router = useRouter();
  const queryClient = useQueryClient();

  useEffect(() => {
    setUnauthorizedHandler(() => {
      queryClient.removeQueries({ queryKey: qk.me() });
      const from = router.state.location.href;
      if (from.startsWith("/login")) return;
      void router.navigate({ to: "/login", search: { redirect: from } });
    });
    return () => setUnauthorizedHandler(null);
  }, [router, queryClient]);
}

function RootLayout() {
  const { queryClient } = Route.useRouteContext();

  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <ToastProvider>
          <AnnouncerProvider>
            <SessionWatch />
            <Outlet />
          </AnnouncerProvider>
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}

// The hook needs the query client from the provider above it, which is why it is not called in
// RootLayout itself.
function SessionWatch() {
  useSessionExpiry();
  return null;
}
