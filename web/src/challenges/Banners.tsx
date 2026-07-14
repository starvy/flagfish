import { Banner, RelativeTime } from "../ui";
import { useClock } from "./clock";

/**
 * The two things the clock can be doing to a player, said out loud.
 *
 * The freeze belongs on the challenge page as much as on the scoreboard: past it, the solve list
 * the page shows is truncated, and a short list that does not say it is short is a lie.
 */
export function ClockBanners() {
  const { paused, frozen, freeze } = useClock();

  return (
    <>
      {paused && (
        <Banner tone="warn" title="the CTF is paused">
          flags are not being judged right now. hints and downloads still work.
        </Banner>
      )}
      {frozen && freeze !== null && (
        <Banner tone="info" title="standings are frozen">
          frozen since <RelativeTime value={freeze} live={false} />. solves after that point are
          hidden from the board and from every solve list.
        </Banner>
      )}
    </>
  );
}
</content>
