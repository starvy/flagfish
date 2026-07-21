import { queryOptions } from "@tanstack/react-query";
import { api, type ScoreboardParams } from "../api/client";
import { qk } from "./keys";

// The board is the one screen people stare at, so it polls. `as_of` reads the standings as
// they stood at an instant, which is how a frozen board is shown without the server lying.
export const scoreboardQuery = (params: ScoreboardParams = {}) =>
  queryOptions({
    queryKey: qk.scoreboard(params),
    queryFn: () => api.scoreboard(params),
    refetchInterval: 10_000,
    staleTime: 10_000,
  });

export const bracketsQuery = queryOptions({
  queryKey: qk.brackets(),
  queryFn: () => api.brackets(),
  staleTime: 5 * 60_000,
});

export const scoreHistoryQuery = (id: number) =>
  queryOptions({
    queryKey: qk.scoreHistory(id),
    queryFn: () => api.scoreHistory(id),
    staleTime: 60_000,
  });
