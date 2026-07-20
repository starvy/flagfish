import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi, type PageParams } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

// The pre-event gauge: utilisation for every unique-flag challenge that has a pool.
export const poolStatsQuery = () =>
  queryOptions({
    queryKey: qk.admin.poolStats(),
    queryFn: () => adminApi.poolStats(),
    staleTime: ADMIN_STALE_TIME,
  });

// One challenge's pool, newest generation first, each row carrying who it was issued to.
export const challengeInstancesQuery = (challengeId: number, params: PageParams = {}) =>
  queryOptions({
    queryKey: qk.admin.instances(challengeId, params),
    queryFn: () => adminApi.listInstances(challengeId, params),
    staleTime: ADMIN_STALE_TIME,
  });

// Uploading a pool changes the challenge's utilisation and its instance list.
export function useUploadInstances() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { challengeId: number; body: Parameters<typeof adminApi.uploadInstances>[1] }) =>
      adminApi.uploadInstances(v.challengeId, v.body),
    onSuccess: (_data, v) => {
      void qc.invalidateQueries({ queryKey: qk.admin.instances(v.challengeId) });
      void qc.invalidateQueries({ queryKey: qk.admin.poolStats() });
    },
  });
}

// Switching a challenge's flag mode changes what players see and, for the pool screens, whether the
// challenge issues per-account flags at all.
export function useSetChallengeFlagMode() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: number; flagMode: "static" | "unique" }) =>
      adminApi.setChallengeFlagMode(v.id, v.flagMode),
    onSuccess: (_data, v) => {
      void qc.invalidateQueries({ queryKey: qk.challenges() });
      void qc.invalidateQueries({ queryKey: qk.admin.poolStats() });
      void qc.invalidateQueries({ queryKey: qk.admin.instances(v.id) });
    },
  });
}
