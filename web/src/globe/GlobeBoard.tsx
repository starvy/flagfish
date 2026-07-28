import { useCallback, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { challengesQuery } from "../queries";
import { Alert, Button, Spinner } from "../ui";
import { Board } from "../challenges/Board";
import { ClockBanners } from "../challenges/Banners";
import { QueryError } from "../challenges/QueryError";
import type { PortalViewProps } from "../views/view";
import { ChallengeDrawer, CountryDrawer } from "./CountryDrawer";
import { countryByChallenge, indexByCode, placeChallenges } from "./countryStates";
import { GlobeScene, type FocusRequest } from "./GlobeScene";
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
  const nameById = useMemo(() => {
    const names = new Map<number, string>();
    for (const country of placement.countries) {
      for (const challenge of country.challenges) names.set(challenge.id, challenge.name);
    }
    return names;
  }, [placement]);

  const pulses = usePulses(byChallenge, { enabled: webgl && board.isSuccess });

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
    return (
      <>
        <Alert tone="warn" title="this browser cannot draw the globe">
          The globe needs WebGL, and this browser has it switched off or unavailable. Here is the
          standard board instead — nothing is missing from it.
        </Alert>
        <Board />
      </>
    );
  }

  const openCountry = open?.kind === "country" ? byCode.get(open.code) : undefined;

  return (
    <>
      <div className="page-head globe-head">
        <h1>challenges</h1>
        <div className="globe-head__controls">
          <Legend />
          <Button
            variant="secondary"
            size="sm"
            data-testid="portal-view-standard"
            onClick={() => selectView("standard")}
          >
            list view
          </Button>
        </div>
      </div>

      <ClockBanners />

      {board.isError && <QueryError error={board.error} onRetry={() => void board.refetch()} />}

      {board.isPending && (
        <div className="globe-loading">
          <Spinner /> <span className="ff-muted">plotting the board…</span>
        </div>
      )}

      {board.isSuccess && (
        <>
          <GlobeScene
            countries={placement.countries}
            pulses={pulses}
            onSelectCountry={onSelectCountry}
            focus={focus}
          />
          <UnassignedPanel challenges={placement.unplaced} />
        </>
      )}

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
    </>
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
