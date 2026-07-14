import { useEffect, useRef, useState, type FormEvent } from "react";
import { isApiError, type AttemptResult, type AttemptStatus } from "../api/client";
import { useAttempt } from "../queries";
import { Alert, Button, Input } from "../ui";
import { attemptsLabel, type Challenge } from "./types";

export interface FlagFormProps {
  challenge: Challenge;
  paused: boolean;
}

/**
 * The hot path.
 *
 * Everything shown here is the server's answer. A wrong flag comes back as a 200 with
 * `status: "incorrect"` and is a verdict, not a failure; the only real errors are a locked
 * challenge, an exhausted attempt budget and a missing challenge. Nothing is drawn before the
 * verdict lands — an optimistic "correct" is a lie about a player's score, and the score is the
 * product. The query layer owns what a solve invalidates.
 */
export function FlagForm({ challenge, paused }: FlagFormProps) {
  const attempt = useAttempt();
  const [flag, setFlag] = useState("");
  const [verdict, setVerdict] = useState<AttemptResult | null>(null);
  const [cooldown, setCooldown] = useState(0);
  const input = useRef<HTMLInputElement>(null);

  // A 429 is a state the form sits in, not an error it reports: the limiter said when to come
  // back, so the form counts down and shuts until then.
  useEffect(() => {
    if (cooldown <= 0) return;
    const t = setTimeout(() => setCooldown(cooldown - 1), 1000);
    return () => clearTimeout(t);
  }, [cooldown]);

  const rateLimited = cooldown > 0;
  const shut = paused || challenge.locked || rateLimited;
  const failure = attempt.error !== null && !rateLimited ? attempt.error : null;

  function submit(e: FormEvent) {
    e.preventDefault();
    const value = flag.trim();
    if (value === "" || shut || attempt.isPending) return;

    setVerdict(null);
    attempt.mutate(
      { id: challenge.id, flag: value },
      {
        onSuccess: (result) => {
          setVerdict(result);
          if (result.status === "correct") setFlag("");
          input.current?.focus();
        },
        onError: (error) => {
          if (isApiError(error) && error.status === 429) setCooldown(error.retryAfter ?? 30);
        },
      },
    );
  }

  return (
    <>
      <form className="flag-form" onSubmit={submit}>
        <Input
          ref={input}
          mono
          className="flag-form__input"
          placeholder="flag{…}"
          aria-label="flag"
          value={flag}
          onChange={(e) => setFlag(e.target.value)}
          disabled={shut}
          spellCheck={false}
          autoComplete="off"
          invalid={verdict !== null && verdict.status === "incorrect"}
        />
        <Button
          type="submit"
          variant="primary"
          loading={attempt.isPending}
          disabled={shut || flag.trim() === ""}
        >
          submit
        </Button>
      </form>

      <p className="flag-form__budget">{attemptsLabel(challenge)}</p>

      {/* The verdict reaches a screen reader whatever the colours do. */}
      <div className="ff-sr-only" role="status" aria-live="polite">
        {verdict === null ? "" : announce(verdict)}
      </div>

      {rateLimited && (
        <Alert className="flag-form__verdict" tone="warn" title="too many attempts">
          try again in {cooldown}s.
        </Alert>
      )}

      {failure !== null && (
        <Alert className="flag-form__verdict" tone="danger" title="not submitted">
          {isApiError(failure) ? failure.detail : "the attempt did not reach the server."}
        </Alert>
      )}

      {verdict !== null && <Verdict verdict={verdict} />}
    </>
  );
}

function Verdict({ verdict }: { verdict: AttemptResult }) {
  switch (verdict.status as AttemptStatus) {
    case "correct":
      return (
        <Alert
          className="flag-form__verdict"
          tone="success"
          title={verdict.first_blood ? undefined : "correct"}
        >
          {verdict.first_blood && <p className="first-blood flag-form__blood">first blood</p>}
          <p>+{verdict.value} points.</p>
        </Alert>
      );
    case "already_solved":
      return (
        <Alert className="flag-form__verdict" tone="info" title="already solved">
          you have scored this one already; the attempt changed nothing.
        </Alert>
      );
    default:
      return (
        <Alert className="flag-form__verdict" tone="danger" title="incorrect">
          not the flag. the attempt was recorded.
        </Alert>
      );
  }
}

function announce(verdict: AttemptResult): string {
  switch (verdict.status as AttemptStatus) {
    case "correct":
      return verdict.first_blood
        ? `First blood! Correct, ${verdict.value} points.`
        : `Correct, ${verdict.value} points.`;
    case "already_solved":
      return "Already solved. Your score did not change.";
    default:
      return "Incorrect.";
  }
}
</content>
