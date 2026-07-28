import { useCallback, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { challengesQuery } from "../queries";
import { Alert, Button, Spinner } from "../ui";
import { Board } from "../challenges/Board";
import { QueryError } from "../challenges/QueryError";
import type { PortalViewProps } from "../views/view";
import { ChallengeDrawer, CountryDrawer } from "./CountryDrawer";
import { countryByChallenge, indexByCode, placeChallenges } from "./countryStates";
import { GlobeScene, type FocusRequest } from "./GlobeScene";
import { SolveTicker } from "./SolveTicker";
import { UnassignedPanel } from "./UnassignedPanel";
import { usePulses } from "./usePulses";
import { supportsWebGL } from "./webgl";
import "./globe.css";

/** What the drawer is showing. A challenge remembers the country it was opened from. */
type Open =
  | { kind: "country"; code: string }
  | { kind: "challenge"; id: number; from: string | null }
  | null;

export function GlobeBoard({ selectView }: PortalViewProps) {
  // Probed once. The answer cannot change while the page is open, and asking per render would
  // allocate a canvas every time.
  const [webgl] = useState(supportsWebGL);

  const board = useQuery(challengesQuery);
  const placement = useMemo(
    () => placeChallenges(board.data?.challenges ?? []),
    [board.data],
  );
  const byCode = useMemo(() => indexByCode(placement.countries), [placement]);
  const byChallenge = useMemo(() => countryByChallenge(placement.countries), [placement]);
  // Every challenge this player was served, placed or not: the drawer titles one, and the ticker
  // has to recognise a solve wherever it landed.
  const nameById = useMemo(() => {
    const names = new Map<number, string>();
    for (const challenge of board.data?.challenges ?? []) names.set(challenge.id, challenge.name);
    return names;
  }, [board.data]);

  const live = webgl && board.isSuccess;
  const pulses = usePulses(byChallenge, { enabled: live });

  const [open, setOpen] = useState<Open>(null);
  const [focus, setFocus] = useState<FocusRequest | null>(null);

  // One click on a pin: a country holding a single challenge opens it, because the drawer in
  // between would list exactly one row and cost a click to say nothing.
  const onSelectCountry = useCallback(
    (code: string) => {
      const country = byCode.get(code);
      if (country === undefined) return;
      setFocus((previous) => ({ code, seq: (previous?.seq ?? 0) + 1 }));
      setOpen(
        country.total === 1
          ? { kind: "challenge", id: country.challenges[0]!.id, from: null }
          : { kind: "country", code },
      );
    },
    [byCode],
  );

  const closeDrawer = useCallback(() => setOpen(null), []);

  if (!webgl) {
    // The shell has handed this view the whole viewport; the standard board is a document and
    // needs its column and its scrollbar back.
    return (
      <div className="globe-fallback">
        <Alert tone="warn" title="this browser cannot draw the globe">
          The globe needs WebGL, and this browser has it switched off or unavailable. Here is the
          standard board instead — nothing is missing from it.
        </Alert>
        <Board />
      </div>
    );
  }

  const openCountry = open?.kind === "country" ? byCode.get(open.code) : undefined;

  return (
    <div className="globe-view">
      {board.isSuccess && (
        <GlobeScene
          countries={placement.countries}
          pulses={pulses}
          onSelectCountry={onSelectCountry}
          focus={focus}
        />
      )}

      {board.isPending && (
        <div className="globe-standin">
          <Spinner /> <span className="ff-muted">plotting the board…</span>
        </div>
      )}

      {board.isError && (
        <div className="globe-standin">
          <QueryError error={board.error} onRetry={() => void board.refetch()} />
        </div>
      )}

      {/* Inert as a whole so a drag between the panels still spins the globe; each panel takes
          the pointer back for itself. */}
      <div className="globe-hud">
        <section className="globe-hud__controls globe-panel">
          <h1 className="globe-panel__label">challenges</h1>
          <Legend />
          <Button
            variant="secondary"
            size="sm"
            data-testid="portal-view-standard"
            onClick={() => selectView("standard")}
          >
            list view
          </Button>
        </section>

        <div className="globe-hud__unplaced">
          <UnassignedPanel challenges={placement.unplaced} />
        </div>

        <div className="globe-hud__ticker">
          <SolveTicker names={nameById} enabled={live} />
        </div>
      </div>

      {openCountry !== undefined && (
        <CountryDrawer
          country={openCountry}
          onClose={closeDrawer}
          onOpenChallenge={(id) => setOpen({ kind: "challenge", id, from: openCountry.code })}
        />
      )}

      {open?.kind === "challenge" && (
        <ChallengeDrawer
          challengeId={open.id}
          title={nameById.get(open.id) ?? "challenge"}
          // Closing goes back to the country it was opened from, not out to the globe: the
          // player was part-way through a list and put one item down.
          onClose={() =>
            setOpen(open.from === null ? null : { kind: "country", code: open.from })
          }
        />
      )}
    </div>
  );
}

function Legend() {
  return (
    <ul className="globe-legend" aria-label="what the colours mean">
      <li>
        <span className="globe-legend__swatch globe-legend__swatch--open" /> unsolved
      </li>
      <li>
        <span className="globe-legend__swatch globe-legend__swatch--partial" /> in progress
      </li>
      <li>
        <span className="globe-legend__swatch globe-legend__swatch--captured" /> captured
      </li>
    </ul>
  );
}
