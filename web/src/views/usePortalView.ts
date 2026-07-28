import { useCallback, useEffect, useMemo, useState } from "react";
import { useInstanceState } from "../shell/instance";
import { readPreference, writePreference } from "./preference";
import { resolvePortalView, warnUnrecognisedView, type ResolvedView } from "./resolve";
import { VIEWS } from "./registry";
import type { PortalView } from "./view";

export interface PortalViewState {
  resolved: ResolvedView;
  /** The player's saved choice: a view id, or null for "following the instance". */
  preference: string | null;
  setPreference: (id: string | null) => void;
  views: readonly PortalView[];
}

/**
 * Which view the board renders, and the switch that changes it.
 *
 * The instance's choice arrives with the rest of the instance snapshot, so before it lands this
 * resolves to the standard board — which is also what an instance that never set one gets. That
 * is deliberate: a moment of the plain board beats a moment of nothing.
 */
export function usePortalView(): PortalViewState {
  const [preference, setPreferenceState] = useState<string | null>(readPreference);
  const { portalView } = useInstanceState();

  const resolved = useMemo(
    () => resolvePortalView({ preference, instanceView: portalView }),
    [preference, portalView],
  );

  useEffect(() => warnUnrecognisedView(resolved.unrecognised), [resolved.unrecognised]);

  const setPreference = useCallback((id: string | null) => {
    writePreference(id);
    setPreferenceState(id);
  }, []);

  return { resolved, preference, setPreference, views: VIEWS };
}
