import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute, useRouter } from "@tanstack/react-router";
import { isApiError } from "../api/client";
import {
  answersFor,
  CustomFieldInputs,
  initialFieldValues,
  type FieldValue,
  type FieldValues,
} from "../lib/customFields";
import { PolicyGate, denialOf, type Denial } from "../policy";
import { registrationFieldsQuery, useRegister } from "../queries";
import { Alert, Button, Card, Field, Form, Input } from "../ui";

export const Route = createFileRoute("/register")({
  component: RegisterPage,
});

function fieldErrorOf(error: unknown, field: string): string | undefined {
  if (!isApiError(error)) return undefined;
  return error.fieldErrors.find((e) => e.location === `body.${field}`)?.message;
}

// A taken email (409), a validation failure (422) and a rate limit keep the form on screen: the
// player can fix all three by typing. A denial — already signed in, registration closed or full —
// replaces it, because there is nothing left to submit.
const INLINE_STATUSES = new Set([409, 422, 429]);

function RegisterPage() {
  const router = useRouter();
  const register = useRegister();

  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  // The custom registration fields, if any. A closed registration denies this the same way it
  // denies the page, so an error here just means "no fields to show"; the base form still works.
  const fieldsQuery = useQuery(registrationFieldsQuery);
  const customFields = fieldsQuery.data?.fields ?? [];
  const [fieldValues, setFieldValues] = useState<FieldValues>({});
  useEffect(() => {
    setFieldValues(initialFieldValues(customFields));
    // Re-seed only when the set of fields changes, not on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fieldsQuery.data]);
  const setFieldValue = (id: number, value: FieldValue) =>
    setFieldValues((v) => ({ ...v, [id]: value }));

  const nameRef = useRef<HTMLInputElement>(null);
  const emailRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);

  const error: unknown = register.error;
  const inline = error === null || (isApiError(error) && INLINE_STATUSES.has(error.status));
  const denial = inline ? null : denialOf(error);

  const nameError = fieldErrorOf(error, "name");
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
    if (nameError !== undefined) nameRef.current?.focus();
    else if (emailError !== undefined) emailRef.current?.focus();
    else if (passwordError !== undefined) passwordRef.current?.focus();
    else alertRef.current?.focus();
  }, [error, nameError, emailError, passwordError]);

  // `already-authed` carries a Location and PolicyGate follows it back to the board; a closed
  // registration is a 404 with nowhere to go, and stays here, said plainly.
  if (denial !== null) {
    return (
      <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
        <PolicyGate error={error} fallback={(d) => <RegisterDenied denial={d} />} />
      </div>
    );
  }

  const submit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    register.mutate(
      {
        name,
        email,
        password,
        fields: answersFor(
          customFields.map((f) => f.id),
          fieldValues,
        ),
      },
      {
        onSuccess: async () => {
          await router.invalidate();
          await router.navigate({ to: "/challenges" });
        },
      },
    );
  };

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "26rem" }}>
      <Card title="Create an account">
        <Form
          onSubmit={submit}
          footer={
            <Button type="submit" variant="primary" fullWidth loading={register.isPending}>
              Create account
            </Button>
          }
        >
          <Field
            name="name"
            label="Name"
            required
            error={nameError}
            hint="The name the scoreboard shows."
          >
            <Input
              ref={nameRef}
              autoComplete="nickname"
              maxLength={128}
              invalid={nameError !== undefined}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>

          <Field name="email" label="Email" required error={emailError}>
            <Input
              ref={emailRef}
              type="email"
              autoComplete="email"
              maxLength={255}
              invalid={emailError !== undefined}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </Field>

          <Field
            name="password"
            label="Password"
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

          {customFields.length > 0 && (
            <CustomFieldInputs
              fields={customFields}
              values={fieldValues}
              onChange={setFieldValue}
            />
          )}

          {message !== null && (
            <div ref={alertRef} tabIndex={-1}>
              <Alert tone="danger" title="Could not create your account">
                {message}
              </Alert>
            </div>
          )}
        </Form>

        <p aria-live="polite" role="status" className="ff-sr-only">
          {register.isPending ? "Creating your account…" : (message ?? "")}
        </p>
      </Card>

      <p className="ff-muted">
        Already have an account? <Link to="/login">Sign in</Link>
      </p>
    </div>
  );
}

function RegisterDenied({ denial }: { denial: Denial }) {
  const closed = denial.reason === "not-found";
  return (
    <Card title={closed ? "Registration is closed" : denial.treatment.title}>
      <Alert tone={closed ? "info" : "warn"} title={denial.treatment.title}>
        {closed
          ? "This instance is not taking new accounts. If you were expecting to sign up, ask the organisers for an invitation."
          : denial.treatment.message}
      </Alert>
      <p className="ff-muted">
        Already have an account? <Link to="/login">Sign in</Link>
      </p>
    </Card>
  );
}
