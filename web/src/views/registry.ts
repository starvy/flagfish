import { BoardSkeleton } from "../challenges/BoardSkeleton";
import type { PortalView } from "./view";

// The registry. Adding a view is one entry here and one component module — nothing else in the
// app names a view, and nothing else has to learn that one exists.
export const VIEWS: readonly PortalView[] = [
  {
    id: "standard",
    label: "list view",
    description: "Categories and cards. Every challenge, sorted by value.",
    load: () => import("../challenges/Board").then((m) => ({ default: m.Board })),
    Fallback: BoardSkeleton,
  },
  {
    id: "globe",
    label: "globe view",
    description: "Challenges placed in the countries they belong to. Solve them all to capture one.",
    load: () => import("../globe/GlobeBoard").then((m) => ({ default: m.GlobeBoard })),
    theme: "nocturne",
    chrome: "immersive",
  },
];

/**
 * The view every fallback ends at. It is the standard board on purpose: it is the one view with
 * no requirement beyond a DOM, so it is always a safe answer.
 */
export const DEFAULT_VIEW: PortalView = VIEWS[0]!;

export function viewById(id: string | null | undefined): PortalView | undefined {
  if (!id) return undefined;
  return VIEWS.find((v) => v.id === id);
}
