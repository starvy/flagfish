import { queryOptions } from "@tanstack/react-query";
import { api } from "../api/client";
import { qk } from "./keys";

// The published-page list behind the nav. Content changes rarely, so a generous stale window keeps
// it out of the way of the game's own traffic.
export const pagesQuery = queryOptions({
  queryKey: qk.pages(),
  queryFn: () => api.pages(),
  staleTime: 60_000,
});

// One page by its slug. A 403 (auth-gated) or 404 (draft/missing) is the ClassPages gate, and the
// route renders it rather than retrying — the verdict will not change on a refetch.
export const pageQuery = (route: string) =>
  queryOptions({
    queryKey: qk.page(route),
    queryFn: () => api.page(route),
    staleTime: 60_000,
    retry: false,
  });
