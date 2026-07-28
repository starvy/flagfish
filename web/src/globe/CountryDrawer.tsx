import { Badge, Dialog } from "../ui";
import { ChallengeDetailBody } from "../challenges/ChallengeDetailBody";
import { solveCountLabel } from "../challenges/types";
import { nameOf } from "./geography";
import type { CountryState } from "./countryStates";

export interface CountryDrawerProps {
  country: CountryState;
  onOpenChallenge: (id: number) => void;
  onClose: () => void;
}

/** What is in one country. Only ever shown for a country holding more than one challenge. */
export function CountryDrawer({ country, onOpenChallenge, onClose }: CountryDrawerProps) {
  return (
    <Dialog
      open
      onClose={onClose}
      title={nameOf(country.code)}
      description={`${country.solved} of ${country.total} solved`}
      className="globe-drawer"
      size="md"
    >
      <ul className="globe-list" data-testid="globe-country-list">
        {country.challenges.map((challenge) => (
          <li key={challenge.id}>
            <button
              type="button"
              className="globe-list__row"
              data-testid={`globe-country-challenge-${challenge.id}`}
              onClick={() => onOpenChallenge(challenge.id)}
            >
              <span className="globe-list__name">{challenge.name}</span>
              <span className="globe-list__meta">
                <span className="ff-muted">{challenge.category}</span>
                <span className="globe-list__value">{challenge.value}</span>
                <span className="ff-muted">{solveCountLabel(challenge.solve_count)}</span>
                {challenge.solved && <Badge tone="success">solved</Badge>}
                {challenge.locked && <Badge tone="warn">locked</Badge>}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </Dialog>
  );
}

export interface ChallengeDrawerProps {
  challengeId: number;
  title: string;
  onClose: () => void;
}

/**
 * A challenge, in a drawer.
 *
 * The same body the challenge's own page renders — submitting, hints and files all work here,
 * because there is only one of it.
 */
export function ChallengeDrawer({ challengeId, title, onClose }: ChallengeDrawerProps) {
  return (
    <Dialog
      open
      onClose={onClose}
      title={title}
      className="globe-drawer globe-drawer--challenge"
      size="lg"
    >
      <div data-testid="globe-challenge-drawer">
        <ChallengeDetailBody challengeId={challengeId} showBackLink={false} headingLevel={2} />
      </div>
    </Dialog>
  );
}
