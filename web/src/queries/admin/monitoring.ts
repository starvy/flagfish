import { queryOptions } from "@tanstack/react-query";
import { adminApi, type StatsParams, type SubmissionsParams } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

// The newest page (no cursor) is what an operator watches live; the screen turns on a
// refetchInterval there and leaves the deeper pages static, so scrolling back stays put.
export const adminSubmissionsQuery = (params: SubmissionsParams = {}) =>
  queryOptions({
    queryKey: qk.admin.submissions(params),
    queryFn: () => adminApi.submissions(params),
    staleTime: ADMIN_STALE_TIME,
  });

export const adminStatsQuery = (params: StatsParams = {}) =>
  queryOptions({
    queryKey: qk.admin.stats(params),
    queryFn: () => adminApi.stats(params),
    staleTime: ADMIN_STALE_TIME,
  });
