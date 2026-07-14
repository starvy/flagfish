import { useEffect, useRef, useState } from "react";
import { Link, createFileRoute } from "@tanstack/react-router";
import { isApiError } from "../api/client";
import { PolicyGate, denialOf } from "../policy";
import { useResetApply } from "../queries";
import { Alert, Button, Card, Field, Form, Input } from "../ui";

export const Route = createFileRoute("/reset-password")({
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token: typeof search.token === "string" && search.token !== "" ? search.token : undefined,
  }),
  component: ResetPasswordPage,
});

function fieldErrorOf(error: unknown, field: string): string | undefined {
  if (!isApiError(error)) return undefined;
  return error.fieldErrors.find((e) => e.location === `body.${field}`)?.message;
}

// A dead link is a 400, and it is a state, not a crash: what the player needs is a new link.
const INLINE_STATUSES = new Set([400, 422, 429]);

function ResetPasswordPage() {
  const { token } = Route.useSearch();
  const apply = useResetApply();

  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  // Only raised on submit: nagging about a mismatch while the second field is half-typed is noise.
  const [mismatch, setMismatch] = useState(false);

  const passwordRef = useRef<HTMLInputElement>(null);
  const confirmRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);
  const doneRef = useRef<HTMLDivElement>(null);

  const error: unknown = apply.error;
  const inline = error === null || (isApiError(error) && INLINE_STATUSES.has(error.status));
  const denial = inline ? null : denialOf(error);

  const deadLink = isApiError(error) && error.status === 400;
  const passwordError = fieldErrorOf(error, "password");
  const tokenError = fieldErrorOf(error, "token");
  const message =
    error === null || denial !== null
      ? null
      : isApiError(error)
        ? (tokenError ?? error.detail)
        : "The server could not be reached.";

  useEffect(() => {
    if (error === null) return;
    if (passwordError !== undefined) passwordRef.current?.focus();
    else alertRef.current?.focus();
  }, [error, passwordError]);

  useEffect(() => {
    if (apply.isSuccess) doneRef.current?.focus();
  }, [apply.isSuccess]);

  if (denial !== null) return <PolicyGate error={error} />;

  // No token at all — a typed-in URL, or a mail client that ate the query string.
  if (token === undefined) {
    return (
      <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
        <Card title="That link is incomplete">
          <Alert tone="warn" title="No reset token">
            This page needs the link from the reset email. Copy it in full, or ask for a new one.
          </Alert>
          <p className="ff-muted">
            <Link to="/forgot-password">Send me a new link</Link> · <Link to="/login">Sign in</Link>
          </p>
        </Card>
      </div>
    );
  }

  if (apply.isSuccess) {
    return (
      <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
        <Card title="Password changed">
          <div ref={doneRef} tabIndex={-1}>
            <Alert tone="success" title="Your password is set">
              Sign in with the new one.
            </Alert>
          </div>
          <p>
            <Link to="/login" className="ff-btn ff-btn--primary">
              Sign in
            </Link>
          </p>
        </Card>
        <p aria-live="polite" role="status" className="ff-sr-only">
          Your password has been changed.
        </p>
      </div>
    );
  }

  const submit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (password !== confirm) {
      setMismatch(true);
      confirmRef.current?.focus();
      return;
    }
    setMismatch(false);
    apply.mutate({ token, password });
  };

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
      <Card title="Set a new password">
        <Form
          onSubmit={submit}
          footer={
            <Button type="submit" variant="primary" fullWidth loading={apply.isPending}>
              Set password
            </Button>
          }
        >
          <Field
            name="password"
            label="New password"
            required
            error={passwordError}
            hint="At least 8 characters."
          >
            <Input
              ref={passwordRef}
              type="password"
              autoComplete="new-password"
              minLength={8}
              maxLength={128}
              invalid={passwordError !== undefined}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>

          <Field
            name="password_confirm"
            label="Confirm new password"
            required
            error={mismatch ? "The two passwords do not match." : undefined}
          >
            <Input
              ref={confirmRef}
              type="password"
              autoComplete="new-password"
              minLength={8}
              maxLength={128}
              invalid={mismatch}
              value={confirm}
              onChange={(e) => {
                setConfirm(e.target.value);
                if (mismatch) setMismatch(false);
              }}
            />
          </Field>

          {message !== null && (
            <div ref={alertRef} tabIndex={-1}>
              <Alert
                tone={deadLink ? "warn" : "danger"}
                title={deadLink ? "That link is no longer good" : "Could not set your password"}
              >
                {message}
                {deadLink && (
                  <>
                    {" "}
                    <Link to="/forgot-password">Ask for a new one.</Link>
                  </>
                )}
              </Alert>
            </div>
          )}
        </Form>

        <p aria-live="polite" role="status" className="ff-sr-only">
          {apply.isPending ? "Setting your password…" : (message ?? "")}
        </p>
      </Card>
    </div>
  );
}
