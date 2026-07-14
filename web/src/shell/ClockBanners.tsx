import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Alert } from "../ui";
import { formatRemaining, useCtfClock, useNow, type Phase } from "./instance";

const absolute = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

function Countdown({ to }: { to: Date }) {
  const now = useNow(1000);
  return (
    <time className="ff-time sh-countdown" dateTime={to.toISOString()}>
      {formatRemaining(to.getTime() - now)}
    </time>
  );
}

function At({ date }: { date: Date }) {
  return (
    <time className="ff-time" dateTime={date.toISOString()}>
      {absolute.format(date)}
    </time>
  );
}

/**
 * Every state the event's clock puts the whole app into. They stack: a paused, frozen event
 * shows both, because they are different facts and hiding one would be a lie of omission.
 */
export function ClockBanners() {
  const { phase, frozen, paused, start, end, freeze, loaded } = useCtfClock();
  const qc = useQueryClient();
  const previous = useRef<Phase | null>(null);

  // Crossing start or end changes what every gated endpoint answers, so the cache it filled
  // while the door was shut is worthless the instant it opens.
  useEffect(() => {
    if (!loaded) return;
    if (previous.current !== null && previous.current !== phase) void qc.invalidateQueries();
    previous.current = phase;
  }, [phase, loaded, qc]);

  if (!loaded) return null;

  return (
    <div className="sh-banners">
      {phase === "before" && start !== undefined && (
        <Alert tone="info" banner title="The CTF has not started">
          Starts in <Countdown to={start} /> — <At date={start} />.
        </Alert>
      )}

      {phase === "ended" && end !== undefined && (
        <Alert tone="warn" banner title="The CTF has ended">
          Play closed <At date={end} />. The board stays readable.
        </Alert>
      )}

      {paused && (
        <Alert tone="danger" banner title="Submissions are paused">
          The organisers paused the event. Flag submissions are refused for everyone, admins
          included; browsing and hint unlocks still work.
        </Alert>
      )}

      {frozen && freeze !== undefined && (
        <Alert tone="warn" banner title="Standings are frozen">
          The scoreboard shows the ranking as it stood at <At date={freeze} />. Solves after that
          instant still count — you just cannot see them move.
        </Alert>
      )}
    </div>
  );
}
