import { queryOptions } from "@tanstack/react-query";
import { api } from "./api/client";

// Public branding drives the theme resolution. It rarely changes and must not spam
// the server, so it stays fresh for a long while and survives a background refetch.
export const instanceQuery = queryOptions({
  queryKey: ["instance"],
  queryFn: () => api.instance(),
  staleTime: 5 * 60_000,
  retry: false,
});

export const meQuery = queryOptions({
  queryKey: ["me"],
  queryFn: () => api.me(),
  staleTime: 60_000,
  retry: false,
});

export const challengesQuery = queryOptions({
  queryKey: ["challenges"],
  queryFn: () => api.challenges(),
  staleTime: 15_000,
});

export const challengeQuery = (id: number) =>
  queryOptions({
    queryKey: ["challenges", id],
    queryFn: () => api.challenge(id),
    staleTime: 15_000,
  });

export const solvesQuery = (id: number) =>
  queryOptions({
    queryKey: ["challenges", id, "solves"],
    queryFn: () => api.challengeSolves(id),
    staleTime: 15_000,
  });

export const scoreboardQuery = queryOptions({
  queryKey: ["scoreboard"],
  queryFn: () => api.scoreboard(),
  refetchInterval: 10_000,
});

export const tokensQuery = queryOptions({
  queryKey: ["tokens"],
  queryFn: () => api.tokens(),
});
