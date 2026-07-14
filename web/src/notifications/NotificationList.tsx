import { Alert, Badge, Button, EmptyState, Markdown, RelativeTime, Skeleton, cx } from "../ui";
import { PolicyGate, denialOf } from "../policy";
import { isApiError } from "../api/client";
import { isFirstBlood, type Notification } from "./feed";
import "./notifications.css";

export interface NotificationListProps {
  items: readonly Notification[];
  /** Ids above this are unread. Omit on a screen that does not mark reads. */
  readThrough?: number;
  /** The drawer wants tight rows; the page wants room to read. */
  dense?: boolean;
}

export function NotificationList({ items, readThrough = 0, dense = false }: NotificationListProps) {
  return (
    <ul className={cx("ff-notif-list", dense && "ff-notif-list--dense")}>
      {items.map((n) => {
        const blood = isFirstBlood(n);
        return (
          <li
            key={n.id}
            className={cx(
              "ff-notif",
              blood && "ff-notif--blood",
              n.id > readThrough && "ff-notif--unread",
            )}
          >
            <div className="ff-notif__head">
              <h3 className="ff-notif__title">{n.title}</h3>
              {blood && <Badge tone="blood">first blood</Badge>}
              <span className="ff-spacer" />
              <RelativeTime value={n.date} className="ff-notif__time" />
            </div>
            <Markdown source={n.content} className="ff-notif__body" />
          </li>
        );
      })}
    </ul>
  );
}

export function NotificationsSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div className="ff-notif-list" aria-busy="true" aria-label="Loading notifications">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="ff-notif">
          <Skeleton width="45%" height="0.9rem" />
          <Skeleton lines={2} />
        </div>
      ))}
    </div>
  );
}

export function NotificationsEmpty({ compact = false }: { compact?: boolean }) {
  return (
    <EmptyState
      title="nothing yet"
      description={
        compact
          ? "Announcements and first bloods land here as they happen."
          : "When the organisers publish an announcement — or someone takes a first blood — it appears here, live."
      }
    />
  );
}

export interface NotificationsErrorProps {
  error: unknown;
  onRetry: () => void;
}

/**
 * A denial is the policy layer's to render — including the ones that redirect. Anything else is
 * a fault the player can retry, and the server's own words say more than a status code would.
 */
export function NotificationsError({ error, onRetry }: NotificationsErrorProps) {
  if (denialOf(error) !== null) return <PolicyGate error={error} />;

  const detail = isApiError(error)
    ? error.detail
    : error instanceof Error
      ? error.message
      : "the request failed";

  return (
    <Alert tone="danger" title="Could not load notifications">
      <p>{detail}</p>
      <Button size="sm" variant="secondary" onClick={onRetry}>
        retry
      </Button>
    </Alert>
  );
}
