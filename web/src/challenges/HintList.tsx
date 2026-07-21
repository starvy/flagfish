import { useState } from "react";
import { isApiError } from "../api/client";
import { useUnlockHint } from "../queries";
import { Alert, Badge, Button, Dialog, EmptyState, Markdown, useToast } from "../ui";
import type { Challenge, ChallengeHint } from "./types";

/**
 * Hints, and the points they cost.
 *
 * An unlock is a charge against the score, so it is never one click: the confirm dialog names the
 * price before it is paid. The three ways it can fail are three different sentences — the player is
 * owed the reason, not a generic "unlock failed".
 */
export function HintList({ challenge }: { challenge: Challenge }) {
  const hints = challenge.hints ?? [];
  const unlock = useUnlockHint();
  const toast = useToast();
  const [asking, setAsking] = useState<ChallengeHint | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  // The unlock response reveals a body before the detail query refetches; the detail carries the body
  // of every hint this account has already unlocked, so a reload still shows it. This session's fresh
  // unlock wins until that refetch lands and starts carrying it too.
  const [revealed, setRevealed] = useState<Record<number, string>>({});

  if (hints.length === 0) {
    return (
      <EmptyState
        title="no hints"
        description="this challenge ships without any — the author left you to it."
      />
    );
  }

  const close = () => {
    setAsking(null);
    setFailure(null);
  };

  const confirm = (hint: ChallengeHint) => {
    setFailure(null);
    unlock.mutate(
      { challengeId: challenge.id, hintId: hint.id },
      {
        onSuccess: (result) => {
          setRevealed((held) => ({ ...held, [result.hint_id]: result.content }));
          close();
          toast.success("hint unlocked", `−${result.charged} points · score is now ${result.score}`);
        },
        onError: (error) => setFailure(unlockFailure(error)),
      },
    );
  };

  return (
    <>
      <ul className="hint-list">
        {hints.map((hint, i) => (
          <HintRow
            key={hint.id}
            hint={hint}
            index={i}
            content={revealed[hint.id] ?? hint.content}
            onAsk={() => setAsking(hint)}
          />
        ))}
      </ul>

      <Dialog
        open={asking !== null}
        onClose={close}
        title="unlock this hint?"
        description={
          asking === null
            ? undefined
            : `It costs ${asking.cost} ${asking.cost === 1 ? "point" : "points"}, taken off your score the moment you confirm. This cannot be undone.`
        }
        size="sm"
        footer={
          <>
            <Button variant="ghost" onClick={close} disabled={unlock.isPending}>
              cancel
            </Button>
            <Button
              variant="primary"
              loading={unlock.isPending}
              onClick={() => asking !== null && confirm(asking)}
            >
              unlock for {asking?.cost ?? 0} pts
            </Button>
          </>
        }
      >
        {failure !== null && (
          <Alert tone="danger" title="not unlocked">
            {failure}
          </Alert>
        )}
      </Dialog>
    </>
  );
}

function HintRow({
  hint,
  index,
  content,
  onAsk,
}: {
  hint: ChallengeHint;
  index: number;
  content: string | undefined;
  onAsk: () => void;
}) {
  return (
    <li className={hint.locked ? "hint-row hint-row--locked" : "hint-row"}>
      <div className="ff-row">
        <span className="hint-row__title">{hint.title ?? `hint ${index + 1}`}</span>
        <Badge tone={hint.unlocked ? "success" : "neutral"}>{hint.cost} pts</Badge>
        {hint.locked && <Badge tone="warn">locked</Badge>}
        <span className="ff-spacer" />
        {hint.unlocked ? (
          <Badge tone="success">unlocked</Badge>
        ) : (
          <Button size="sm" variant="secondary" disabled={hint.locked} onClick={onAsk}>
            unlock
          </Button>
        )}
      </div>

      {hint.locked && (
        <p className="hint-row__note muted">unlock the hints above it before this one.</p>
      )}

      {hint.unlocked &&
        (content === undefined ? (
          <p className="hint-row__note muted">bought already — reload to read it.</p>
        ) : (
          <Markdown className="hint-row__body" source={content} />
        ))}
    </li>
  );
}

/** Each failure is its own sentence. 402, 409 and 403 mean genuinely different things here. */
function unlockFailure(error: unknown): string {
  if (!isApiError(error)) return "the unlock did not reach the server.";
  switch (error.status) {
    case 402:
      return "you do not have enough points to unlock this hint.";
    case 409:
      return "this hint is already unlocked.";
    case 403:
      // "prerequisite hints must be unlocked first", or the teams-mode teamless gate.
      return error.detail;
    case 404:
      return "that hint no longer exists.";
    default:
      return error.detail;
  }
}
