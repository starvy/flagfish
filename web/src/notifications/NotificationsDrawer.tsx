import { useEffect, useRef } from "react";
import { Link } from "@tanstack/react-router";
import { Dialog } from "../ui";
import { newestId } from "./feed";
import {
  NotificationList,
  NotificationsEmpty,
  NotificationsError,
  NotificationsSkeleton,
} from "./NotificationList";
import { closeNotifications, markReadThrough, useNotificationsUI } from "./store";
import { useNotificationToasts } from "./toasts";
import { useNotifications } from "./useNotifications";
import "./notifications.css";

/**
 * The drawer, and the session's one subscriber to the stream.
 *
 * The shell mounts this once, unconditionally: it is what holds the EventSource open and what
 * turns arriving notifications into toasts, so it has to keep running with the panel shut. Only
 * the panel is conditional, and that is <Dialog>'s business — focus trap, Esc, focus restored to
 * the bell.
 */
export function NotificationsDrawer() {
  const { open, readThrough } = useNotificationsUI();
  const feed = useNotifications();
  useNotificationToasts(feed);

  const newest = newestId(feed.items);
  const newestRef = useRef(newest);
  newestRef.current = newest;

  // Reading is closing, not opening: while the panel is up the unread rows have to stay marked
  // as unread, or they un-highlight under the reader's eyes. The cursor moves on the way out.
  useEffect(() => {
    if (!open) return;
    return () => markReadThrough(newestRef.current);
  }, [open]);

  return (
    <Dialog
      open={open}
      onClose={closeNotifications}
      title="notifications"
      description={feed.status === "open" ? "live" : "reconnecting…"}
      className="ff-notif-drawer"
      footer={
        <Link
          to="/notifications"
          search={{ page: 1 }}
          className="ff-btn ff-btn--secondary ff-btn--sm"
          onClick={closeNotifications}
        >
          view all →
        </Link>
      }
    >
      {feed.pending ? (
        <NotificationsSkeleton />
      ) : feed.error !== null && feed.items.length === 0 ? (
        <NotificationsError error={feed.error} onRetry={feed.refetch} />
      ) : feed.items.length === 0 ? (
        <NotificationsEmpty compact />
      ) : (
        <NotificationList items={feed.items} readThrough={readThrough} dense />
      )}
    </Dialog>
  );
}
