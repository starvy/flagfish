// The portal-view module's public surface. A view is *how the challenge board is drawn*; it has
// nothing to do with a colour theme, which is what `theme/` means by the word.
export { ViewOutlet } from "./ViewOutlet";
export { usePortalView, type PortalViewState } from "./usePortalView";
export { VIEWS, DEFAULT_VIEW, viewById } from "./registry";
export { resolvePortalView, type ResolveInput, type ResolvedView, type ViewSource } from "./resolve";
export { readPreference, writePreference } from "./preference";
export type { PortalView, PortalViewProps } from "./view";
