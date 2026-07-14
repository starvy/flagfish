import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi, type AuditParams, type IPOverlapParams, type PageParams } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

export const adminAuditQuery = (params: AuditParams = {}) =>
  queryOptions({
    queryKey: qk.admin.audit(params),
    queryFn: () => adminApi.listAudit(params),
    staleTime: ADMIN_STALE_TIME,
  });

export const flagSharingQuery = (params: PageParams = {}) =>
  queryOptions({
    queryKey: qk.admin.flagSharing(params),
    queryFn: () => adminApi.flagSharing(params),
    staleTime: ADMIN_STALE_TIME,
  });

export const ipOverlapQuery = (params: IPOverlapParams = {}) =>
  queryOptions({
    queryKey: qk.admin.ipOverlap(params),
    queryFn: () => adminApi.ipOverlap(params),
    staleTime: ADMIN_STALE_TIME,
  });

export const accountReportQuery = (id: number) =>
  queryOptions({
    queryKey: qk.admin.accountReport(id),
    queryFn: () => adminApi.accountReport(id),
    staleTime: ADMIN_STALE_TIME,
  });

// Publishing a notification pushes it down the SSE stream on its own; the paged history is
// what needs dropping.
export function useCreateNotification() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: adminApi.createNotification,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.notifications() });
    },
  });
}
