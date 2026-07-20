import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

export const adminPagesQuery = queryOptions({
  queryKey: qk.admin.pages(),
  queryFn: () => adminApi.listPages(),
  staleTime: ADMIN_STALE_TIME,
});

export const adminPageQuery = (id: number) =>
  queryOptions({
    queryKey: qk.admin.page(id),
    queryFn: () => adminApi.getPage(id),
    staleTime: ADMIN_STALE_TIME,
  });

function usePageWrite<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.admin.pages() });
      // Publishing, renaming or deleting a page changes what the public nav and page views serve.
      void qc.invalidateQueries({ queryKey: qk.pages() });
    },
  });
}

export function useCreatePage() {
  return usePageWrite(adminApi.createPage);
}

export function useUpdatePage() {
  return usePageWrite((v: { id: number; body: Parameters<typeof adminApi.updatePage>[1] }) =>
    adminApi.updatePage(v.id, v.body),
  );
}

export function useDeletePage() {
  return usePageWrite((id: number) => adminApi.deletePage(id));
}
