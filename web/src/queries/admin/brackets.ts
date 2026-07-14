import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

export const adminBracketsQuery = queryOptions({
  queryKey: qk.admin.brackets(),
  queryFn: () => adminApi.listBrackets(),
  staleTime: ADMIN_STALE_TIME,
});

function useBracketWrite<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.admin.brackets() });
      // Brackets are how the public board is sliced.
      void qc.invalidateQueries({ queryKey: qk.brackets() });
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
    },
  });
}

export function useCreateBracket() {
  return useBracketWrite(adminApi.createBracket);
}

export function useUpdateBracket() {
  return useBracketWrite((v: { id: number; body: Parameters<typeof adminApi.updateBracket>[1] }) =>
    adminApi.updateBracket(v.id, v.body),
  );
}

export function useDeleteBracket() {
  return useBracketWrite((id: number) => adminApi.deleteBracket(id));
}
