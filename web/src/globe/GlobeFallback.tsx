import { Spinner } from "../ui";

/**
 * What stands in while the globe's chunk downloads — the largest one this app ships, so it is on
 * screen for a moment on a slow line. It ships in the eager chunk, which is why it is a spinner
 * and not a skeleton of a scene, and why it borrows the shell's centring rather than the globe's
 * own stylesheet: that arrives with the chunk it is waiting for.
 */
export function GlobeFallback() {
  return (
    <div className="sh-standin">
      <Spinner /> <span className="ff-muted">loading the globe…</span>
    </div>
  );
}
