import { useEffect, useRef, useState } from "react";
import { Link, createFileRoute } from "@tanstack/react-router";
import { isApiError } from "../api/client";
import { PolicyGate, denialOf } from "../policy";
import { useResetRequest } from "../queries";
import { Alert, Button, Card, Field, Form, Input } from "../ui";

export const Route = createFileRoute("/forgot-password")({
  component: ForgotPasswordPage,
});

function fieldErrorOf(error: unknown, field: string): string | undefined {
  if (!isApiError(error)) return undefined;
  return error.fieldErrors.find((e) => e.location === `body.${field}`)?.message;
}

const INLINE_STATUSES = new Set([422, 429]);

function ForgotPasswordPage() {
  const request = useResetRequest();
  const [email, setEmail] = useState("");

  const emailRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);
  const doneRef = useRef<HTMLDivElement>(null);

  const error: unknown = request.error;
  const inline = error === null || (isApiError(error) && INLINE_STATUSES.has(error.status));
  const denial = inline ? null : denialOf(error);

  const emailError = fieldErrorOf(error, "email");
  const message =
    error === null || denial !== null
      ? null
      : isApiError(error)
        ? error.detail
        : "The server could not be reached.";

  useEffect(() => {
    if (error === null) return;
    if (emailError !== undefined) emailRef.current?.focus();
    else alertRef.current?.focus();
  }, [error, emailError]);

  useEffect(() => {
    if (request.isSuccess) doneRef.current?.focus();
  }, [request.isSuccess]);

  if (denial !== null) return <PolicyGate error={error} />;

  // The server answers 200 for an address it has never seen, and so do we: telling the caller
  // which addresses have accounts is an oracle, and the confirmation is the same either way.
  if (request.isSuccess) {
    return (
      <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
        <Card title="Check your inbox">
          <div ref={doneRef} tabIndex={-1}>
            <Alert tone="success" title="If that address has an account, a reset link is on its way">
              The link expires. If it does not arrive, check your spam folder, then try again.
            </Alert>
          </div>
          <p className="ff-muted">
            <Link to="/login">Back to sign in</Link>
          </p>
        </Card>
        <p aria-live="polite" role="status" className="ff-sr-only">
          If that address has an account, a reset link has been sent.
        </p>
      </div>
    );
  }

  const submit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    request.mutate({ email });
  };

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
      <Card title="Reset your password">
        <p className="ff-muted">
          Give us the address on the account and we will send a link to set a new password.
        </p>
        <Form onSubmit={submit}>
          <Field label="Email" htmlFor="forgot-email" required error={emailError}>
            <Input
              id="forgot-email"
              ref={emailRef}
              type="email"
              name="email"
              autoComplete="email"
              required
              maxLength={255}
              value={email}
              aria-invalid={emailError !== undefined}
              onChange={(e) => setEmail(e.target.value)}
            />
          </Field>

          {message !== null && (
            <div ref={alertRef} tabIndex={-1}>
              <Alert tone="danger" title="Could not send the link">
                {message}
              </Alert>
            </div>
          )}

          <div className="ff-form__footer">
            <Button type="submit" variant="primary" block loading={request.isPending}>
              Send the reset link
            </Button>
          </div>
        </Form>

        <p aria-live="polite" role="status" className="ff-sr-only">
          {request.isPending ? "Sending…" : (message ?? "")}
        </p>
      </Card>

      <p className="ff-muted">
        Remembered it? <Link to="/login">Sign in</Link>
      </p>
    </div>
  );
}
