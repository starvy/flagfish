import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { routeTree } from "./routeTree.gen";
import { setUnauthorizedHandler } from "./api/client";
import { ThemeProvider } from "./theme/provider";
import { bootTheme } from "./theme/boot";
import "./styles.css";

// Resolve and apply the theme before React renders so the first paint is themed.
bootTheme();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: 1, refetchOnWindowFocus: false },
  },
});

const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: "intent",
  defaultPendingComponent: () => <div className="center">loading…</div>,
  defaultErrorComponent: ({ error }) => <div className="center error">{error.message}</div>,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

// A 401 from any endpoint means the session died under us; drop the cached
// identity so the auth guard re-checks instead of trusting a stale /me.
setUnauthorizedHandler(() => {
  queryClient.removeQueries({ queryKey: ["me"] });
  void router.navigate({ to: "/login", search: { redirect: location.pathname } });
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <RouterProvider router={router} />
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
);
