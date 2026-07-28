import type { ComponentType } from "react";

/** What a view is handed. Every view gets the switch, so any of them can offer a way out. */
export interface PortalViewProps {
  /**
   * Save the player's own choice of view. `null` clears it and hands the instance default back
   * the decision — there is no third state to confuse a player with.
   */
  selectView: (id: string | null) => void;
}

/**
 * One way to render the challenge board.
 *
 * `load` is a dynamic import on purpose: a view is free to be expensive — the globe drags in a
 * WebGL engine — because nothing but the view itself is in that chunk, and a player who never
 * sees it never downloads it.
 */
export interface PortalView {
  /** The wire value: what `portal_view` on the instance config names. */
  id: string;
  label: string;
  /** One line, shown wherever a human picks a view. */
  description: string;
  load: () => Promise<{ default: ComponentType<PortalViewProps> }>;
  /** Rendered while `load` is in flight. Must be cheap: it ships in the eager chunk. */
  Fallback?: ComponentType;
}
