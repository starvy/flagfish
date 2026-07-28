import { useState } from "react";
import { ChallengeCard } from "../challenges/ChallengeCard";
import type { BoardChallenge } from "../challenges/types";

/**
 * Everything the map could not place.
 *
 * This panel is not a nicety — it is the promise that choosing a view never costs a player a
 * challenge. A challenge with no country annotation lands here, and so does a locked one, whose
 * annotations the server sends as `{}` precisely so a view cannot leak where it is.
 */
export function UnassignedPanel({ challenges }: { challenges: readonly BoardChallenge[] }) {
  const [open, setOpen] = useState(false);

  if (challenges.length === 0) return null;

  return (
    <section className="globe-unassigned globe-panel" data-testid="globe-unassigned">
      <button
        type="button"
        className="globe-unassigned__head globe-panel__label"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span className="globe-unassigned__caret" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
        not on the map
        <span className="ff-muted">
          {challenges.length} {challenges.length === 1 ? "challenge" : "challenges"}
        </span>
      </button>

      {open && (
        <div className="globe-unassigned__body board__grid">
          {challenges.map((challenge) => (
            <ChallengeCard key={challenge.id} challenge={challenge} />
          ))}
        </div>
      )}
    </section>
  );
}
