import { createContext, use, useCallback, useRef, useState, type ReactNode } from "react";

type Announce = (message: string) => void;

const AnnouncerContext = createContext<Announce | null>(null);

/**
 * Announce an async result to a screen reader: a correct flag, a hint charged, a team joined.
 * A colour change is not a result — this is how the shell keeps its promise that every one of
 * them is spoken.
 */
export function useAnnounce(): Announce {
  return use(AnnouncerContext) ?? (() => {});
}

export function AnnouncerProvider({ children }: { children: ReactNode }) {
  // Two slots, written alternately: a live region only speaks when its text *changes*, so the
  // same message twice in a row would be silent the second time.
  const [slots, setSlots] = useState<[string, string]>(["", ""]);
  const next = useRef(0);

  const announce = useCallback<Announce>((message) => {
    const slot = next.current;
    next.current = slot === 0 ? 1 : 0;
    setSlots((current) => (slot === 0 ? [message, current[1]] : [current[0], message]));
  }, []);

  return (
    <AnnouncerContext value={announce}>
      {children}
      <div className="ff-sr-only" aria-live="polite" aria-atomic="true">
        {slots[0]}
      </div>
      <div className="ff-sr-only" aria-live="polite" aria-atomic="true">
        {slots[1]}
      </div>
    </AnnouncerContext>
  );
}
