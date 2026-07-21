import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi, type SearchParams } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

export const adminTeamsQuery = (params: SearchParams = {}) =>
  queryOptions({
    queryKey: qk.admin.teams(params),
    queryFn: () => adminApi.listTeams(params),
    staleTime: ADMIN_STALE_TIME,
  });

export const adminTeamQuery = (id: number) =>
  queryOptions({
    queryKey: qk.admin.team(id),
    queryFn: () => adminApi.getTeam(id),
    staleTime: ADMIN_STALE_TIME,
  });

function useTeamWrite<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.admin.teams() });
      // A ban or hide moves the board and the public team pages.
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
      void qc.invalidateQueries({ queryKey: qk.teams() });
    },
  });
}

export function useCreateAdminTeam() {
  return useTeamWrite(adminApi.createTeam);
}

export function useUpdateAdminTeam() {
  return useTeamWrite(
    (v: { id: number; body: Parameters<typeof adminApi.updateTeam>[1] }) =>
      adminApi.updateTeam(v.id, v.body),
  );
}

// Banning your own team is a 409: the instance must never be left without a usable admin.
export function useSetTeamBanned() {
  return useTeamWrite((v: { id: number; banned: boolean }) =>
    adminApi.setTeamBanned(v.id, v.banned),
  );
}

export function useSetTeamHidden() {
  return useTeamWrite((v: { id: number; hidden: boolean }) =>
    adminApi.setTeamHidden(v.id, v.hidden),
  );
}

export const adminTeamMembersQuery = (id: number) =>
  queryOptions({
    queryKey: qk.admin.teamMembers(id),
    queryFn: () => adminApi.listTeamMembers(id),
    staleTime: ADMIN_STALE_TIME,
  });

/**
 * A roster edit touches both teams' rosters, member counts and public pages. It deliberately does
 * not invalidate the scoreboard: solves and awards are stamped with the team that earned them, so
 * moving a player changes who they score for next and leaves every standing exactly where it was.
 */
function useRosterWrite<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      // The prefix covers every team's detail and roster, so the destination refreshes too.
      void qc.invalidateQueries({ queryKey: qk.admin.teams() });
      void qc.invalidateQueries({ queryKey: qk.teams() });
    },
  });
}

export function useRemoveTeamMember(teamId: number) {
  return useRosterWrite((userId: number) => adminApi.removeTeamMember(teamId, userId));
}

export function useMoveTeamMember(teamId: number) {
  return useRosterWrite((v: { userId: number; toTeamId: number }) =>
    adminApi.moveTeamMember(teamId, v.userId, v.toTeamId),
  );
}
