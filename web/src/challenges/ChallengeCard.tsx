import { Link } from "@tanstack/react-router";
import { Badge, cx } from "../ui";
import { solveCountLabel, type BoardChallenge } from "./types";

/**
 * One challenge on the board.
 *
 * It is an anchor, not a div with a click handler, so Enter opens it and the browser gives the
 * focus ring, the middle-click and the context menu for free.
 */
export function ChallengeCard({ challenge }: { challenge: BoardChallenge }) {
  const { name, value, solved, locked, solve_count, tags } = challenge;

  return (
    <Link
      to="/challenges/$challengeId"
      params={{ challengeId: challenge.id }}
      className={cx(
        "ff-card",
        "chal-card",
        solved && "chal-card--solved",
        locked && "chal-card--locked",
      )}
    >
      <div className="chal-card__head">
        <span className="chal-card__name">{name}</span>
        <span className="chal-card__value">{value}</span>
      </div>

      {(tags ?? []).length > 0 && (
        <div className="chal-card__tags">
          {(tags ?? []).map((tag) => (
            <Badge key={tag} tone="neutral">
              {tag}
            </Badge>
          ))}
        </div>
      )}

      <div className="chal-card__foot">
        {/* A redacted count is null and renders as an em dash. Zero means nobody has solved it. */}
        <span title={solve_count === null ? "solve counts are hidden on this instance" : undefined}>
          {solveCountLabel(solve_count)}
        </span>
        <span className="ff-row">
          {solved && <Badge tone="success">solved</Badge>}
          {locked && <Badge tone="warn">locked</Badge>}
        </span>
      </div>

      {locked && <p className="chal-card__note">solve its prerequisites to unlock this one.</p>}
    </Link>
  );
}
