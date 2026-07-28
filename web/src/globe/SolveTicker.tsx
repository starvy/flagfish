import { useMemo } from "react";
import { useSolveStream } from "../lib/solves";
import { cx } from "../ui";

/** How many solves the strip holds. It is a glance, not a log — the solve list is the record. */
const SHOWN = 5;

interface Entry {
  id: number;
  name: string;
  firstBlood: boolean;
}

/**
 * The live solve feed, as one line of an ops console.
 *
 * A solve naming a challenge this board does not list is dropped without a word: the server
 * already decided what this player may see, and the feed carries an id, not a permission. There
 * is nothing to be loud about — nothing was lost, and the solve is still in the solves table.
 */
export function SolveTicker({
  names,
  enabled = true,
}: {
  /** Challenge id → name, from the board this player was served. */
  names: ReadonlyMap<number, string>;
  enabled?: boolean;
}) {
  const { events } = useSolveStream({ enabled });

  const entries = useMemo(() => {
    const out: Entry[] = [];
    for (const event of events) {
      const name = names.get(event.challenge_id);
      if (name === undefined) continue;
      out.push({ id: event.solve_id, name, firstBlood: event.first_blood });
      if (out.length === SHOWN) break;
    }
    return out;
  }, [events, names]);

  return (
    <div className="globe-ticker" data-testid="solve-ticker">
      <span className="globe-ticker__tag">feed</span>
      <ul className="globe-ticker__list" aria-live="polite" aria-label="recent solves">
        {entries.length === 0 && <li className="globe-ticker__idle">awaiting solves</li>}
        {entries.map((entry) => (
          <li
            key={entry.id}
            className={cx("globe-ticker__row", entry.firstBlood && "globe-ticker__row--blood")}
          >
            <span className="globe-ticker__kind">
              {entry.firstBlood ? "first blood" : "solve"}
            </span>
            <span className="globe-ticker__sep" aria-hidden="true">
              ·
            </span>
            <span className="globe-ticker__name">{entry.name}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
