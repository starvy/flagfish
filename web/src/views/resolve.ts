import { DEFAULT_VIEW, viewById } from "./registry";
import type { PortalView } from "./view";

export type ViewSource = "preference" | "instance" | "default";

export interface ResolveInput {
  /** The player's saved choice, or null/undefined for "never answered". */
  preference?: string | null;
  /** `portal_view` from the instance config. */
  instanceView?: string | null;
}

export interface ResolvedView {
  view: PortalView;
  source: ViewSource;
  /**
   * The instance asked for a view this build does not ship. The resolution already fell back —
   * this is here so the caller can say so out loud, because a board that silently ignores the
   * admin's setting looks exactly like a board that is working.
   */
  unrecognised: string | null;
}

/**
 * The whole resolution order, as one pure function: the player's own choice wins, then the
 * instance default, then the standard board.
 *
 * A preference naming a view this build does not ship is dropped in silence — it is a stale value
 * in one browser, and the next source is the right answer. An unrecognised *instance* view is not
 * silent: it is a setting an operator made for everybody, and it is not being honoured.
 */
export function resolvePortalView(input: ResolveInput): ResolvedView {
  const preferred = viewById(input.preference);
  if (preferred) return { view: preferred, source: "preference", unrecognised: null };

  const instance = viewById(input.instanceView);
  if (instance) return { view: instance, source: "instance", unrecognised: null };

  const asked = input.instanceView ?? "";
  return {
    view: DEFAULT_VIEW,
    source: "default",
    unrecognised: asked === "" ? null : asked,
  };
}

const warned = new Set<string>();

/** Complains once per unknown id. Once, because this runs on every render of the board. */
export function warnUnrecognisedView(id: string | null): void {
  if (id === null || warned.has(id)) return;
  warned.add(id);
  console.warn(
    `flagfish: the instance asks for portal view "${id}", which this build does not ship. ` +
      `Falling back to "${DEFAULT_VIEW.id}".`,
  );
}
