import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute, useRouter } from "@tanstack/react-router";
import { isApiError, type Instance } from "../api/client";
import { PolicyGate, denialOf } from "../policy";
import { instanceQuery, useLogin } from "../queries";
import { Alert, Button, Card, Field, Form, Input } from "../ui";

export const Route = createFileRoute("/login")({
  validateSearch: (search: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof search.redirect === "string" ? search.redirect : undefined,
  }),
  component: LoginPage,
});

// An attacker-chosen `?redirect=` is an open-redirect primitive, and a freshly authenticated
// session is exactly what it wants to land on. Only a path on this origin is a destination.
function safeRedirect(to: string | undefined): string {
  return to !== undefined && to.startsWith("/") && !to.startsWith("//") ? to : "/challenges";
}

/**
 * Whether to hide the way to `/register`: `private` and `mlc` instances have no public form.
 *
 * An unrecognised value means "we do not know" — the field is missing from the generated schema
 * and the server currently garbles it — and an unknown must not hide the front door. The real
 * gate is the server's 404 on `POST /register`, which `/register` renders honestly.
 */
function registrationHidden(instance: Instance | undefined): boolean {
  if (instance === undefined) return false;
  const vis: unknown = (instance as Record<string, unknown>).registration_visibility;
  return vis === "private" || vis === "mlc";
}

function fieldErrorOf(error: unknown, field: string): string | undefined {
  if (!isApiError(error)) return undefined;
  return error.fieldErrors.find((e) => e.location === `body.${field}`)?.message;
}

// A 401 here is this form's error — the email or the password is wrong — not a dead session,
// which is why the client marks the login call `localUnauthorized`. It, a 422 and a rate limit
// all keep the typed-in form on screen; every other denial belongs to the policy layer.
const INLINE_STATUSES = new Set([401, 422, 429]);

function LoginPage() {
  const { redirect } = Route.useSearch();
  const router = useRouter();
  const { data: instance } = useQuery(instanceQuery);
  const login = useLogin();

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const emailRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);

  const error: unknown = login.error;
  const inline = error === null || (isApiError(error) && INLINE_STATUSES.has(error.status));
  const denial = inline ? null : denialOf(error);

  const emailError = fieldErrorOf(error, "email");
  const passwordError = fieldErrorOf(error, "password");
  const message =
    error === null || denial !== null
      ? null
      : isApiError(error)
        ? error.detail
        : "The server could not be reached.";

  useEffect(() => {
    if (error === null) return;
    if (emailError !== undefined) emailRef.current?.focus();
    else if (passwordError !== undefined) passwordRef.current?.focus();
    else alertRef.current?.focus();
  }, [error, emailError, passwordError]);

  // An instance that is not set up denies login itself, with a Location; PolicyGate follows it.
  if (denial !== null) return <PolicyGate error={error} />;

  const submit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    login.mutate(
      { email, password },
      {
        onSuccess: async () => {
          await router.invalidate();
          await router.navigate({ href: safeRedirect(redirect) });
        },
      },
    );
  };

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
      <Card title="Sign in">
        <Form
          onSubmit={submit}
          footer={
            <Button type="submit" variant="primary" fullWidth loading={login.isPending}>
              Sign in
            </Button>
          }
        >
          <Field name="email" label="Email" required error={emailError}>
            <Input
              ref={emailRef}
              type="email"
              autoComplete="username"
              maxLength={255}
              invalid={emailError !== undefined}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </Field>

          <Field name="password" label="Password" required error={passwordError}>
            <Input
              ref={passwordRef}
              type="password"
              autoComplete="current-password"
              invalid={passwordError !== undefined}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>

          {message !== null && (
            <div ref={alertRef} tabIndex={-1}>
              <Alert tone="danger" title="Could not sign you in">
                {message}
              </Alert>
            </div>
          )}
        </Form>

        <p aria-live="polite" role="status" className="ff-sr-only">
          {login.isPending ? "Signing in…" : (message ?? "")}
        </p>
      </Card>

      <p className="ff-muted">
        <Link to="/forgot-password">Forgot your password?</Link>
        {!registrationHidden(instance) && (
          <>
            {" · No account? "}
            <Link to="/register">Register</Link>
          </>
        )}
      </p>
    </div>
  );
}
