import { useEffect, useRef, useState } from "react";
import { Link, createFileRoute, useRouter } from "@tanstack/react-router";
import { isApiError } from "../api/client";
import { PolicyGate, denialOf } from "../policy";
import { useChangePassword } from "../queries";
import { Alert, Button, Card, Field, Form, Input } from "../ui";

export const Route = createFileRoute("/change-password")({
  component: ChangePasswordPage,
});

/**
 * Where an organizer's forced password change sends you.
 *
 * It lives outside the authenticated shell on purpose. The wall blocks `/me`, which is what
 * the shell loads before it renders anything — so a change form inside the shell is a form
 * nobody under the wall can reach. Everything here rides on `POST /me/password`, the one
 * route the wall exempts, and the credential it asks for is the one the caller just logged
 * in with. No mailbox is involved: an organizer with a broken mailer can still force a
 * change without locking the account out of the event.
 */

function fieldErrorOf(error: unknown, field: string): string | undefined {
  if (!isApiError(error)) return undefined;
  return error.fieldErrors.find((e) => e.location === `body.${field}`)?.message;
}

// A wrong current password is a 401 this form owns; the rest is the policy layer's.
const INLINE_STATUSES = new Set([400, 401, 422, 429]);

function ChangePasswordPage() {
  const router = useRouter();
  const change = useChangePassword();

  const [current, setCurrent] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  // Only raised on submit: nagging about a mismatch while the second field is half-typed is noise.
  const [mismatch, setMismatch] = useState(false);

  const currentRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const confirmRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);

  const error: unknown = change.error;
  const inline = error === null || (isApiError(error) && INLINE_STATUSES.has(error.status));
  const denial = inline ? null : denialOf(error);

  const currentError = fieldErrorOf(error, "current_password");
  const passwordError = fieldErrorOf(error, "new_password");
  const message =
    error === null || denial !== null
      ? null
      : isApiError(error)
        ? error.detail
        : "The server could not be reached.";

  useEffect(() => {
    if (error === null) return;
    if (currentError !== undefined) currentRef.current?.focus();
    else if (passwordError !== undefined) passwordRef.current?.focus();
    else alertRef.current?.focus();
  }, [error, currentError, passwordError]);

  if (denial !== null) return <PolicyGate error={error} />;

  const submit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (password !== confirm) {
      setMismatch(true);
      confirmRef.current?.focus();
      return;
    }
    setMismatch(false);
    change.mutate(
      { current_password: current, new_password: password },
      // The wall is down the moment this succeeds, so go where the user was headed.
      { onSuccess: () => void router.navigate({ to: "/challenges" }) },
    );
  };

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
      <Card title="Choose a new password">
        <Alert tone="warn" title="A new password is required">
          An organiser has asked you to change your password. Until you do, the rest of the site
          is closed to you.
        </Alert>

        {/* Said before the change, not after: this page navigates away on success, and a warning
            the user never reads is the same as no warning. */}
        <p className="ff-muted">
          Changing your password signs out your other sessions and revokes every API token on this
          account. Scripts and CI using one will need a replacement from Settings.
        </p>

        <Form
          onSubmit={submit}
          footer={
            <Button type="submit" variant="primary" fullWidth loading={change.isPending}>
              Change password
            </Button>
          }
        >
          <Field name="current_password" label="Current password" required error={currentError}>
            <Input
              ref={currentRef}
              type="password"
              autoComplete="current-password"
              maxLength={128}
              invalid={currentError !== undefined}
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </Field>

          <Field
            name="new_password"
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
            name="new_password_confirm"
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
              <Alert tone="danger" title="Could not change your password">
                {message}
              </Alert>
            </div>
          )}
        </Form>

        <p className="ff-muted">
          Signed in as someone else? <Link to="/login">Sign in again</Link>.
        </p>

        <p aria-live="polite" role="status" className="ff-sr-only">
          {change.isPending ? "Changing your password…" : (message ?? "")}
        </p>
      </Card>
    </div>
  );
}
