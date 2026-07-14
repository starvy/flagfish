import { useEffect, useRef, useState } from "react";
import { Link, createFileRoute, useRouter } from "@tanstack/react-router";
import { isApiError } from "../api/client";
import { PolicyGate, denialOf } from "../policy";
import { useVerifyConfirm, useVerifyResend } from "../queries";
import { Alert, Button, Card } from "../ui";

export const Route = createFileRoute("/confirm")({
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token: typeof search.token === "string" && search.token !== "" ? search.token : undefined,
  }),
  component: ConfirmPage,
});

/**
 * Seconds left on a 429, ticking down.
 *
 * Keyed on the error object rather than the number, so two rate limits in a row restart the
 * clock instead of silently reusing the first one's.
 */
function useRetryAfter(error: unknown): number {
  const seconds = isApiError(error) && error.status === 429 ? (error.retryAfter ?? 60) : 0;
  const [left, setLeft] = useState(seconds);

  useEffect(() => {
    setLeft(seconds);
    if (seconds === 0) return;
    const id = window.setInterval(() => setLeft((n) => Math.max(0, n - 1)), 1000);
    return () => window.clearInterval(id);
  }, [error, seconds]);

  return left;
}

function ConfirmPage() {
  const { token } = Route.useSearch();
  return token === undefined ? <CheckYourInbox /> : <ConfirmToken token={token} />;
}

function ConfirmToken({ token }: { token: string }) {
  const router = useRouter();
  const confirm = useVerifyConfirm();
  const { mutate } = confirm;

  // The token is single-use: a second POST would 400 and turn a success into an error on
  // screen. StrictMode remounts effects, so the guard is not optional.
  const submitted = useRef(false);
  useEffect(() => {
    if (submitted.current) return;
    submitted.current = true;
    mutate({ token });
  }, [mutate, token]);

  const error: unknown = confirm.error;
  const deadLink = isApiError(error) && error.status === 400;
  const denial = error === null || deadLink || (isApiError(error) && error.status === 429)
    ? null
    : denialOf(error);

  useEffect(() => {
    if (!confirm.isSuccess) return;
    void router.navigate({ to: "/challenges" });
  }, [confirm.isSuccess, router]);

  if (denial !== null) return <PolicyGate error={error} />;

  const message =
    error === null
      ? null
      : isApiError(error)
        ? error.detail
        : "The server could not be reached.";

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "30rem" }}>
      <Card title="Confirming your email">
        {confirm.isPending && <p className="ff-muted">Checking the link…</p>}

        {confirm.isSuccess && (
          <Alert tone="success" title="Your email is verified">
            Taking you to the challenges.
          </Alert>
        )}

        {message !== null && (
          <>
            <Alert
              tone={deadLink ? "warn" : "danger"}
              title={deadLink ? "That link is no longer good" : "Could not confirm your email"}
            >
              {message}
            </Alert>
            <p className="ff-muted">
              Verification links expire. Sign in and ask for a new one from{" "}
              <Link to="/confirm">the verification page</Link>.
            </p>
          </>
        )}

        <p aria-live="polite" role="status" className="ff-sr-only">
          {confirm.isPending
            ? "Confirming your email…"
            : confirm.isSuccess
              ? "Your email is verified."
              : (message ?? "")}
        </p>
      </Card>
    </div>
  );
}

function CheckYourInbox() {
  const resend = useVerifyResend();
  const error: unknown = resend.error;

  const alreadyVerified = isApiError(error) && error.status === 409;
  const rateLimited = isApiError(error) && error.status === 429;
  const denial = error === null || alreadyVerified || rateLimited ? null : denialOf(error);
  const cooldown = useRetryAfter(error);

  const alertRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (error !== null || resend.isSuccess) alertRef.current?.focus();
  }, [error, resend.isSuccess]);

  // Resending needs a session; an anonymous caller is denied with a Location to /login.
  if (denial !== null) return <PolicyGate error={error} />;

  const message = alreadyVerified
    ? "Your email is already verified."
    : rateLimited
      ? cooldown > 0
        ? `Too many requests — try again in ${cooldown}s.`
        : "Too many requests. Try again now."
      : error !== null
        ? isApiError(error)
          ? error.detail
          : "The server could not be reached."
        : resend.isSuccess
          ? "A new verification link is on its way."
          : null;

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "30rem" }}>
      <Card title="Check your inbox">
        <p>
          We sent a verification link to the address on your account. Open it and the rest of the
          CTF unlocks.
        </p>

        {message !== null && (
          <div ref={alertRef} tabIndex={-1}>
            <Alert
              tone={alreadyVerified ? "info" : rateLimited ? "warn" : error !== null ? "danger" : "success"}
              title={
                alreadyVerified
                  ? "Nothing to do"
                  : rateLimited
                    ? "Slow down"
                    : error !== null
                      ? "Could not resend the link"
                      : "Link sent"
              }
            >
              {message}
            </Alert>
          </div>
        )}

        <div className="ff-form__footer">
          <Button
            variant="primary"
            loading={resend.isPending}
            disabled={alreadyVerified || cooldown > 0}
            onClick={() => resend.mutate()}
          >
            {cooldown > 0 ? `Resend in ${cooldown}s` : "Resend the link"}
          </Button>
          {alreadyVerified && (
            <Link to="/challenges">
              <Button variant="secondary">Play</Button>
            </Link>
          )}
        </div>

        <p aria-live="polite" role="status" className="ff-sr-only">
          {resend.isPending ? "Sending a new link…" : (message ?? "")}
        </p>
      </Card>

      <p className="ff-muted">
        Wrong address, or no email? Ask an organiser — they can verify you by hand.
      </p>
    </div>
  );
}
