import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

// The manual adjustments on one scoring account, newest first. accountId is a team id in teams mode
// and a user id in users mode; the server resolves which from the instance.
export const adminAwardsQuery = (accountId: number) =>
  queryOptions({
    queryKey: qk.admin.awards(accountId),
    queryFn: () => adminApi.listAwards(accountId),
    staleTime: ADMIN_STALE_TIME,
  });

// A grant or a revoke moves the ledger, so it moves the board and the account's own list. Invalidate
// that account's award list, the scoreboard, and the public team/user surfaces.
function useAwardWrite<TVars, TData>(accountId: number, fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.admin.awards(accountId) });
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
      void qc.invalidateQueries({ queryKey: qk.teams() });
    },
  });
}

export function useGrantAward(accountId: number) {
  return useAwardWrite(accountId, (v: { value: number; reason: string }) =>
    adminApi.grantAward({ account_id: accountId, value: v.value, reason: v.reason }),
  );
}

export function useRevokeAward(accountId: number) {
  return useAwardWrite(accountId, (id: number) => adminApi.revokeAward(id));
}
