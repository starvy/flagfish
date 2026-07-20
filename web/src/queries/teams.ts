import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, isApiError, type Team } from "../api/client";
import { qk } from "./keys";

/**
 * The caller's team, or null when they have none.
 *
 * The server answers 404 for a teamless player. That is an enrollment state — the normal one,
 * for everybody who has not joined yet — so it is data, not an error, and it must not be
 * allowed to fall into an error boundary.
 */
export const myTeamQuery = queryOptions({
  queryKey: qk.myTeam(),
  queryFn: async (): Promise<Team | null> => {
    try {
      return await api.myTeam();
    } catch (e) {
      if (isApiError(e) && e.status === 404) return null;
      throw e;
    }
  },
  staleTime: 60_000,
  retry: false,
});

export const teamQuery = (id: number) =>
  queryOptions({
    queryKey: qk.team(id),
    queryFn: () => api.team(id),
    staleTime: 60_000,
  });

function useTeamMutation<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.myTeam() });
      void qc.invalidateQueries({ queryKey: qk.teams() });
      // Team membership decides what the board and the challenges show.
      void qc.invalidateQueries({ queryKey: qk.me() });
      void qc.invalidateQueries({ queryKey: qk.challenges() });
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
    },
  });
}

export function useCreateTeam() {
  return useTeamMutation(api.createTeam);
}

export function useJoinTeam() {
  return useTeamMutation(api.joinTeam);
}

export function useLeaveTeam() {
  return useTeamMutation(() => api.leaveTeam());
}

// Captain-only; the server refuses everyone else, so no client-side gate is load-bearing.
export function useUpdateMyTeam() {
  return useTeamMutation(api.updateMyTeam);
}
