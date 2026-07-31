import { Suspense, lazy, type ComponentType, type LazyExoticComponent } from "react";
import { Skeleton } from "../ui";
import { usePortalView } from "./usePortalView";
import type { PortalView, PortalViewProps } from "./view";

// One lazy wrapper per view, made once. Building it during render would hand React a new component
// type every pass, and a new type is a remount — the globe would tear down and rebuild its scene
// on every keystroke anywhere above it.
const loaded = new Map<string, LazyExoticComponent<ComponentType<PortalViewProps>>>();

function componentFor(view: PortalView): LazyExoticComponent<ComponentType<PortalViewProps>> {
  const hit = loaded.get(view.id);
  if (hit) return hit;
  const made = lazy(view.load);
  loaded.set(view.id, made);
  return made;
}

/**
 * The switch point: resolve which view this player gets, then render it.
 *
 * This is the only place in the app that turns a view id into a component, which is what keeps
 * "add a view" down to a registry entry and a module.
 */
export function ViewOutlet() {
  const { resolved, setPreference } = usePortalView();
  const View = componentFor(resolved.view);
  const Fallback = resolved.view.Fallback ?? DefaultFallback;

  return (
    <Suspense fallback={<Fallback />}>
      <View selectView={setPreference} />
    </Suspense>
  );
}

function DefaultFallback() {
  return <Skeleton height="24rem" />;
}
