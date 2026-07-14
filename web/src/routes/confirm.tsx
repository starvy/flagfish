import { useEffect, useRef, useState } from "react";
import { Link, createFileRoute, useRouter } from "@tanstack/react-router";
import { isApiError } from "../api/client";
import { PolicyGate, denialOf } from "../policy";
import { useVerifyConfirm, useVerifyResend } from "../queries";
import { Alert, Button, Card, Spinner } from "../ui";

export const Route = createFileRoute("/confirm")({
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token: typeof search.token === "string" && search.token !== "" ? search.token : undefined,
  }),
  component: ConfirmPage,
});

/**
 * Seconds left on a rate limit, ticking down.
 *
 * Keyed on the error object rather than on the number, so a second 429 restarts the clock
 * instead of silently reusing the first one's.
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

  // The token is single-use: a second POST answers 400 and would turn a success on screen into
  // a failure. StrictMode re-runs effects, so the guard is not optional.
  const submitted = useRef(false);
  useEffect(() => {
    if (submitted.current) return;
    submitted.current = true;
    mutate({ token });
  }, [mutate, token]);

  useEffect(() => {
    if (confirm.isSuccess) void router.navigate({ to: "/challenges" });
  }, [confirm.isSuccess, router]);

  const error: unknown = confirm.error;
  const deadLink = isApiError(error) && error.status === 400;
  const inline = error === null || deadLink || (isApiError(error) && error.status === 429);
  const denial = inline ? null : denialOf(error);

  if (denial !== null) return <PolicyGate error={error} />;

  const message =
    error === null ? null : isApiError(error) ? error.detail : "The server could not be reached.";

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "30rem" }}>
      <Card title="Confirming your email">
        {confirm.isPending && (
          <p className="ff-row ff-muted">
            <Spinner size="sm" label="Confirming" /> Checking the link…
          </p>
        )}

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
              Verification links expire. Sign in and ask for a fresh one from{" "}
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
  const inline = error === null || alreadyVerified || rateLimited;
  const denial = inline ? null : denialOf(error);
  const cooldown = useRetryAfter(error);

  const noticeRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (error !== null || resend.isSuccess) noticeRef.current?.focus();
  }, [error, resend.isSuccess]);

  // Resending needs a session, and an anonymous caller is denied with a Location to /login.
  if (denial !== null) return <PolicyGate error={error} />;

  const message = alreadyVerified
    ? "Your email is already verified."
    : rateLimited
      ? cooldown > 0
        ? `Too many requests — try again in ${cooldown}s.`
        : "Too many requests. You can try again now."
      : error !== null
        ? isApiError(error)
          ? error.detail
          : "The server could not be reached."
        : resend.isSuccess
          ? "A fresh verification link is on its way."
          : null;

  const tone = alreadyVerified ? "info" : rateLimited ? "warn" : error !== null ? "danger" : "success";
  const title = alreadyVerified
    ? "Nothing to do"
    : rateLimited
      ? "Slow down"
      : error !== null
        ? "Could not resend the link"
        : "Link sent";

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "30rem" }}>
      <Card title="Check your inbox">
        <p>
          We sent a verification link to the address on your account. Open it and the rest of the
          CTF unlocks.
        </p>

        {message !== null && (
          <div ref={noticeRef} tabIndex={-1}>
            <Alert tone={tone} title={title}>
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
            <Link to="/challenges" className="ff-btn ff-btn--secondary">
              Play
            </Link>
          )}
        </div>

        <p aria-live="polite" role="status" className="ff-sr-only">
          {resend.isPending ? "Sending a fresh link…" : (message ?? "")}
        </p>
      </Card>

      <p className="ff-muted">
        Wrong address, or nothing arriving? Ask an organiser — they can verify you by hand.
      </p>
    </div>
  );
}
