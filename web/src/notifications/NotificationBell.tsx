import { Button, cx } from "../ui";
import { toggleNotifications, useNotificationsUI } from "./store";
import { useNotifications } from "./useNotifications";
import "./notifications.css";

export interface NotificationBellProps {
  className?: string;
}

/** The shell's trigger for the drawer, carrying the unread count. */
export function NotificationBell({ className }: NotificationBellProps) {
  const { unread } = useNotifications();
  const { open } = useNotificationsUI();

  return (
    <Button
      variant="ghost"
      className={cx("ff-notif-bell", className)}
      onClick={toggleNotifications}
      aria-haspopup="dialog"
      aria-expanded={open}
      // The badge is decoration; the count has to reach a screen reader through the name.
      aria-label={unread === 0 ? "Notifications" : `Notifications, ${unread} unread`}
    >
      <BellGlyph />
      {unread > 0 && (
        <span className="ff-notif-bell__count" aria-hidden="true">
          {unread > 99 ? "99+" : unread}
        </span>
      )}
    </Button>
  );
}

// currentColor, so the glyph is the theme's text colour wherever the shell puts it.
function BellGlyph() {
  return (
    <svg
      className="ff-notif-bell__glyph"
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d="M8 1.5a4 4 0 0 0-4 4v2.2L2.8 10a.6.6 0 0 0 .5.9h9.4a.6.6 0 0 0 .5-.9L12 7.7V5.5a4 4 0 0 0-4-4Z" />
      <path d="M6.4 13a1.7 1.7 0 0 0 3.2 0" />
    </svg>
  );
}
