import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { notificationsQuery } from "../queries";
import { useNotificationStream, type StreamStatus } from "../lib/notifications";
import { mergeNotifications, newestId, type Notification } from "./feed";
import { useNotificationsUI } from "./store";

// One page is what the drawer shows, and it is the same 50 the stream replays — enough that a
// player who has been away sees the same history either way.
const BACKFILL = { page: 1, per_page: 50 } as const;

export interface Feed {
  /** Stream and backfill merged on id, newest first. */
  items: readonly Notification[];
  unread: number;
  status: StreamStatus;
  /** The backfill's first answer has landed — success or failure. */
  settled: boolean;
  /** The newest id the backfill knew about. 0 before it answers, or when it answers empty. */
  backfilledThrough: number;
  pending: boolean;
  error: unknown;
  refetch: () => void;
}

/**
 * The drawer's and the bell's view of the world: the live stream, backfilled by the paged
 * history, with the read cursor applied.
 *
 * The stream is live-only — it replays, but it is not the archive — so a fresh session would
 * otherwise open on an empty drawer with a full history sitting one GET away.
 */
export function useNotifications(): Feed {
  const { readThrough } = useNotificationsUI();
  const { events, status } = useNotificationStream();
  const query = useQuery(notificationsQuery(BACKFILL));

  const page = query.data?.notifications;
  const items = useMemo(() => mergeNotifications(events, page), [events, page]);

  const unread = useMemo(() => items.filter((n) => n.id > readThrough).length, [items, readThrough]);

  return {
    items,
    unread,
    status,
    settled: query.isSuccess || query.isError,
    backfilledThrough: newestId(page ?? []),
    pending: query.isPending && items.length === 0,
    error: query.isError ? query.error : null,
    refetch: () => void query.refetch(),
  };
}
