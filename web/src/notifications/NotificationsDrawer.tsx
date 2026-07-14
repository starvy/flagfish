import { useEffect } from "react";
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

  // Opening the drawer is the read: there is no server-side read state to reconcile with.
  useEffect(() => {
    if (open && newest > 0) markReadThrough(newest);
  }, [open, newest]);

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
        // The cursor is read once, on open: rows must not un-highlight under the reader's eyes.
        <NotificationList items={feed.items} readThrough={open ? readThrough : newest} dense />
      )}
    </Dialog>
  );
}
