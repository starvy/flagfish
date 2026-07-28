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
  /**
   * A colour theme this view brings with it, by name. While this view is the resolved one the
   * whole app wears it — a board that is a lit sphere in space cannot sit inside a white page and
   * still be one thing. The player's escape is the switch back to the standard board, not a
   * palette that fights the view.
   */
  theme?: string;
  /**
   * The board wants the viewport, not a column in the document: the shell drops its width and
   * padding and the header floats over the content. Only the board route — a challenge's own page
   * stays a page.
   */
  chrome?: "immersive";
}
