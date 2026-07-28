import { useEffect, useMemo, useSyncExternalStore } from "react";
import { useInstanceState } from "../shell/instance";
import { readPreference, subscribePreference, writePreference } from "./preference";
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

/** The saved choice, live. Every caller reads the one store, so a write anywhere lands everywhere. */
export function usePortalViewPreference(): string | null {
  return useSyncExternalStore(subscribePreference, readPreference, readPreference);
}

/**
 * Which view this player gets.
 *
 * The instance's choice arrives with the rest of the instance snapshot, so before it lands this
 * resolves to the standard board — which is also what an instance that never set one gets. That
 * is deliberate: a moment of the plain board beats a moment of nothing.
 */
export function useResolvedPortalView(): ResolvedView {
  const preference = usePortalViewPreference();
  const { portalView } = useInstanceState();

  const resolved = useMemo(
    () => resolvePortalView({ preference, instanceView: portalView }),
    [preference, portalView],
  );

  useEffect(() => warnUnrecognisedView(resolved.unrecognised), [resolved.unrecognised]);

  return resolved;
}

/** The resolved view plus the switch that changes it. */
export function usePortalView(): PortalViewState {
  const resolved = useResolvedPortalView();
  const preference = usePortalViewPreference();

  return { resolved, preference, setPreference: writePreference, views: VIEWS };
}
