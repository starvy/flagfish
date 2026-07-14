import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

export const adminConfigQuery = queryOptions({
  queryKey: qk.admin.config(),
  queryFn: () => adminApi.getConfig(),
  staleTime: ADMIN_STALE_TIME,
});

export function useUpdateConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: adminApi.updateConfig,
    onSuccess: (config) => {
      qc.setQueryData(qk.admin.config(), config);
      // The clock, the theme and the visibilities all live here — every player-facing read
      // is downstream of this write.
      void qc.invalidateQueries({ queryKey: qk.instance() });
      void qc.invalidateQueries({ queryKey: qk.challenges() });
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
    },
  });
}
