// The portal-view module's public surface. A view is *how the challenge board is drawn*; it has
// nothing to do with a colour theme, which is what `theme/` means by the word.
export { ViewOutlet } from "./ViewOutlet";
export { usePortalView, useResolvedPortalView, type PortalViewState } from "./usePortalView";
export { VIEWS, DEFAULT_VIEW, viewById } from "./registry";
export { resolvePortalView, type ResolveInput, type ResolvedView, type ViewSource } from "./resolve";
// The preference store is not part of the surface: reads and writes go through usePortalView,
// and a caller who takes the store directly is holding a copy that React does not know about.
export type { PortalView, PortalViewProps } from "./view";
