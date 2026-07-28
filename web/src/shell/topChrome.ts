import { useLayoutEffect, useState, type RefObject } from "react";

/**
 * Publishes the height of the shell's top chrome as `--sh-hud-top` on the app element.
 *
 * The immersive layout lays the header and the clock banners over the main area, so anything a
 * view floats inside main has to know how far down it may start. Measuring is the only honest
 * answer: the header wraps when the nav outgrows one line, the banners come and go with the
 * clock, and a number that guesses either of them is a transparent box sitting on top of a
 * control the player then cannot click.
 *
 * Returns the ref to put on the chrome. Writing the variable cannot change what is being measured
 * — nothing above reads it — so the observer cannot drive itself.
 */
export function useTopChromeHeight(
  app: RefObject<HTMLElement | null>,
  enabled: boolean,
): (node: HTMLElement | null) => void {
  const [chrome, setChrome] = useState<HTMLElement | null>(null);

  // Layout effect: the first measurement must land before paint, or a cached view chunk gets one
  // frame at the CSS fallback offset.
  useLayoutEffect(() => {
    const host = app.current;
    if (host === null) return;
    if (!enabled || chrome === null) {
      host.style.removeProperty("--sh-hud-top");
      return;
    }

    const publish = () => {
      const height = Math.ceil(chrome.getBoundingClientRect().height);
      host.style.setProperty("--sh-hud-top", `${height}px`);
    };
    publish();

    const observer = new ResizeObserver(publish);
    observer.observe(chrome);
    return () => {
      observer.disconnect();
      host.style.removeProperty("--sh-hud-top");
    };
  }, [app, chrome, enabled]);

  return setChrome;
}
