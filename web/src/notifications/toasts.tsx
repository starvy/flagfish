import { useEffect, useRef } from "react";
import { Badge, Button, useToast } from "../ui";
import { excerpt, isFirstBlood, newestId } from "./feed";
import { openNotifications } from "./store";
import type { Feed } from "./useNotifications";

const DWELL = 6_000;
const DWELL_BLOOD = 12_000;

/**
 * Turns arriving notifications into toasts.
 *
 * The hard part is not toasting the past. On connect the server replays its last 50, and on every
 * reconnect it replays them again — a client that toasts whatever the stream hands it greets a
 * reload with a wall of old announcements. So nothing toasts until the backfill has answered, and
 * the backfill's newest id is the waterline: everything at or below it is history the player could
 * already have read, everything above it was published while they were watching.
 *
 * A notification published between the backfill and the stream's first frame is above the
 * waterline and does toast — which is right: it is new to this player.
 */
export function useNotificationToasts(feed: Feed): void {
  const { toast } = useToast();
  const { items, settled, backfilledThrough, error } = feed;

  const waterline = useRef<number | null>(null);
  const toastedThrough = useRef(0);

  useEffect(() => {
    if (!settled) return;

    if (waterline.current === null) {
      // A failed backfill leaves no waterline to draw, so fall back to whatever the replay has
      // already delivered: silence beats fifty toasts for announcements the player has read.
      waterline.current = error === null ? backfilledThrough : newestId(items);
      return;
    }

    const floor = Math.max(waterline.current, toastedThrough.current);

    // Oldest first, so a burst lands in the order it was published.
    for (const n of [...items].reverse()) {
      if (n.id <= floor) continue;
      const blood = isFirstBlood(n);
      toast({
        tone: "info",
        title: n.title,
        duration: blood ? DWELL_BLOOD : DWELL,
        message: (
          <>
            {blood && <Badge tone="blood">first blood</Badge>} {excerpt(n.content)}
          </>
        ),
        action: (
          <Button variant="ghost" size="sm" onClick={openNotifications}>
            open
          </Button>
        ),
      });
      toastedThrough.current = n.id;
    }
  }, [items, settled, backfilledThrough, error, toast]);
}
