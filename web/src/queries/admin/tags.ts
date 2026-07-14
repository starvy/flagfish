import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

export const adminTagsQuery = queryOptions({
  queryKey: qk.admin.tags(),
  queryFn: () => adminApi.listTags(),
  staleTime: ADMIN_STALE_TIME,
});

function useTagWrite<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.admin.tags() });
      void qc.invalidateQueries({ queryKey: qk.challenges() });
    },
  });
}

export function useMergeTag() {
  return useTagWrite((v: { value: string; into: string }) => adminApi.mergeTag(v.value, v.into));
}

// A tag still attached to challenges is a 409 unless `force` detaches it from all of them.
export function useDeleteTag() {
  return useTagWrite((v: { value: string; force?: boolean }) =>
    adminApi.deleteTag(v.value, v.force ?? false),
  );
}
