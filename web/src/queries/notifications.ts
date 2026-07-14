import { queryOptions } from "@tanstack/react-query";
import { api, type NotificationsParams } from "../api/client";
import { qk } from "./keys";

// The paged history. The live feed arrives over SSE (useNotificationStream); this is what
// backfills it and what a drawer pages through.
export const notificationsQuery = (params: NotificationsParams = {}) =>
  queryOptions({
    queryKey: qk.notifications(params),
    queryFn: () => api.notifications(params),
    staleTime: 30_000,
  });
