import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

export const adminFieldsQuery = queryOptions({
  queryKey: qk.admin.fields(),
  queryFn: () => adminApi.listFields(),
  staleTime: ADMIN_STALE_TIME,
});

function useFieldWrite<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.admin.fields() });
      // A field change alters the registration form and the /me editor a player sees.
      void qc.invalidateQueries({ queryKey: qk.registrationFields() });
      void qc.invalidateQueries({ queryKey: qk.me() });
    },
  });
}

export function useCreateField() {
  return useFieldWrite(adminApi.createField);
}

export function useUpdateField() {
  return useFieldWrite((v: { id: number; body: Parameters<typeof adminApi.updateField>[1] }) =>
    adminApi.updateField(v.id, v.body),
  );
}

export function useDeleteField() {
  return useFieldWrite((id: number) => adminApi.deleteField(id));
}
