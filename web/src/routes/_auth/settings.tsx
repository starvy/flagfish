import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  isApiError,
  type CreatedToken,
  type Me,
  type MeField,
  type TokenListItem,
} from "../../api/client";
import {
  answersFor,
  CustomFieldInputs,
  initialFieldValues,
  type FieldValue,
  type FieldValues,
} from "../../lib/customFields";
import {
  meQuery,
  tokensQuery,
  useAnswerFields,
  useChangeEmail,
  useChangeName,
  useChangePassword,
  useCreateToken,
  useDeleteToken,
  useUpdateMe,
} from "../../queries";
import { denialOf, PolicyGate } from "../../policy";
import { ThemeSwitcher } from "../../theme/ThemeSwitcher";
import {
  Alert,
  Badge,
  Button,
  Card,
  CodeBlock,
  ConfirmDestructive,
  DataTable,
  Dialog,
  EmptyState,
  Field,
  fieldErrors,
  Form,
  Input,
  RelativeTime,
  Select,
  Skeleton,
  Tabs,
  useToast,
  type Column,
  type FieldErrors,
} from "../../ui";

const TABS = ["profile", "security", "tokens"] as const;
type Tab = (typeof TABS)[number];

function isTab(value: unknown): value is Tab {
  return typeof value === "string" && (TABS as readonly string[]).includes(value);
}

export const Route = createFileRoute("/_auth/settings")({
  // The `incomplete-profile` denial lands here with no tab, so the default must be the one
  // that explains the account.
  validateSearch: (search: Record<string, unknown>): { tab: Tab } => ({
    tab: isTab(search.tab) ? search.tab : "profile",
  }),
  component: SettingsPage,
});

function SettingsPage() {
  const { tab } = Route.useSearch();
  const navigate = Route.useNavigate();

  return (
    <>
      <div className="page-head">
        <h1>settings</h1>
      </div>
      <Tabs
        label="Settings"
        value={tab}
        onChange={(id) => void navigate({ search: { tab: id as Tab }, replace: true })}
        items={[
          { id: "profile", label: "Profile", content: <Profile /> },
          { id: "security", label: "Security", content: <Security /> },
          { id: "tokens", label: "API tokens", content: <Tokens /> },
        ]}
      />
    </>
  );
}

/* ------------------------------------------------------------------ profile */

function Profile() {
  const { data: me, isPending, error, refetch } = useQuery(meQuery);

  if (isPending) {
    return (
      <Card>
        <Skeleton lines={4} />
      </Card>
    );
  }
  if (error) return <LoadFailure error={error} onRetry={() => void refetch()} title="Could not load your account" />;

  return (
    <div className="ff-stack">
      <Card title="Account">
        <dl className="ff-stack">
          <Row label="Role">{me.is_admin ? <Badge tone="accent">admin</Badge> : me.role}</Row>
          <Row label="Account id">
            <span className="ff-mono">{me.user_id}</span>
          </Row>
          <Row label="Team">
            {me.team_id === undefined ? (
              <span className="muted">none</span>
            ) : (
              <Link to="/team">your team</Link>
            )}
          </Row>
          <Row label="Public profile">
            <Link to="/users/$userId" params={{ userId: me.user_id }}>
              how others see you
            </Link>
          </Row>
        </dl>
      </Card>

      <IdentityForm me={me} />

      <ProfileForm me={me} />

      {(me.fields ?? []).length > 0 && <CustomFields me={me} />}

      <Card title="Appearance">
        <ThemeSwitcher />
      </Card>
    </div>
  );
}

const PROFILE_FIELDS = [
  { key: "website", label: "Website", hint: "Shown on public profiles." },
  { key: "affiliation", label: "Affiliation", hint: "School, employer, crew." },
  { key: "country", label: "Country", hint: "" },
] as const;

// A short menu of common language tags. It is a convenience, not a limit: any well-formed
// BCP 47 tag the server accepts is valid, and an imported preference outside this list is
// preserved as its own option below rather than silently dropped. When message catalogs
// ship, the list narrows to what is actually translated.
const LANGUAGES: readonly { value: string; label: string }[] = [
  { value: "en", label: "English" },
  { value: "de", label: "Deutsch" },
  { value: "es", label: "Español" },
  { value: "fr", label: "Français" },
  { value: "it", label: "Italiano" },
  { value: "nl", label: "Nederlands" },
  { value: "pl", label: "Polski" },
  { value: "pt-BR", label: "Português (Brasil)" },
  { value: "ru", label: "Русский" },
  { value: "uk", label: "Українська" },
  { value: "tr", label: "Türkçe" },
  { value: "ja", label: "日本語" },
  { value: "ko", label: "한국어" },
  { value: "zh", label: "中文" },
];

/**
 * Identity — display name and email. Name and email are not profile fields: a name change is
 * immediate, an email change is a re-verification. The live email never moves until the address is
 * confirmed from a link mailed to it, so the form shows the pending address as "check your inbox"
 * rather than pretending the change already took.
 */
function IdentityForm({ me }: { me: Me }) {
  const toast = useToast();
  const changeName = useChangeName();
  const changeEmail = useChangeEmail();

  const [name, setName] = useState(me.name);
  const [email, setEmail] = useState("");

  useEffect(() => {
    setName(me.name);
  }, [me.name]);

  const submitName = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const next = name.trim();
    if (next === "" || next === me.name) return;
    changeName.mutate({ name: next }, { onSuccess: () => toast.success("Name changed") });
  };

  const submitEmail = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const next = email.trim();
    if (next === "") return;
    changeEmail.mutate(
      { email: next },
      {
        onSuccess: () => {
          setEmail("");
          toast.success("Check your inbox", `Confirm the change from the link sent to ${next}.`);
        },
      },
    );
  };

  return (
    <Card title="Identity">
      <div className="ff-stack">
        <Form
          onSubmit={submitName}
          error={changeName.error ? messageOf(changeName.error) : undefined}
          errors={fieldsOf(changeName.error)}
          footer={
            <Button type="submit" variant="primary" loading={changeName.isPending} disabled={name.trim() === me.name}>
              Change name
            </Button>
          }
        >
          <Field name="name" label="Display name" hint="Shown on the scoreboard and your public profile.">
            <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={128} required />
          </Field>
        </Form>

        <Form
          onSubmit={submitEmail}
          error={changeEmail.error ? messageOf(changeEmail.error) : undefined}
          errors={fieldsOf(changeEmail.error)}
          footer={
            <Button type="submit" variant="primary" loading={changeEmail.isPending} disabled={email.trim() === ""}>
              Change email
            </Button>
          }
        >
          <Field
            name="email"
            label="Email"
            hint={
              me.verified
                ? "Current: " + me.email
                : "Current: " + me.email + " (not verified)"
            }
          >
            <Input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="new address"
              maxLength={255}
              autoComplete="email"
            />
          </Field>
        </Form>

        {me.pending_email !== undefined && (
          <Alert tone="info" title="Email change pending">
            A confirmation was sent to <span className="ff-mono">{me.pending_email}</span>. Your
            address stays <span className="ff-mono">{me.email}</span> until you open that link.
          </Alert>
        )}
      </div>
    </Card>
  );
}

/** The player-owned fields. Submitting sends them all; an emptied box clears its field. */
function ProfileForm({ me }: { me: Me }) {
  const toast = useToast();
  const update = useUpdateMe();
  const [values, setValues] = useState<Record<string, string>>({
    website: me.website ?? "",
    affiliation: me.affiliation ?? "",
    country: me.country ?? "",
    language: me.language ?? "",
  });

  useEffect(() => {
    setValues({
      website: me.website ?? "",
      affiliation: me.affiliation ?? "",
      country: me.country ?? "",
      language: me.language ?? "",
    });
  }, [me]);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const clean = (s: string) => {
      const t = s.trim();
      return t === "" ? null : t;
    };
    update.mutate(
      {
        website: clean(values.website),
        affiliation: clean(values.affiliation),
        country: clean(values.country),
        language: clean(values.language),
      },
      { onSuccess: () => toast.success("Profile saved") },
    );
  };

  // Keep an imported preference that is not in the common menu (e.g. "zh-Hant") selectable.
  const known = LANGUAGES.some((l) => l.value === values.language);
  const languageOptions = [
    { value: "", label: "Browser default" },
    ...LANGUAGES,
    ...(values.language !== "" && !known ? [{ value: values.language, label: values.language }] : []),
  ];

  return (
    <Card title="Profile">
      <Form
        onSubmit={submit}
        error={update.error ? messageOf(update.error) : undefined}
        errors={fieldsOf(update.error)}
        footer={
          <Button type="submit" variant="primary" loading={update.isPending}>
            Save profile
          </Button>
        }
      >
        {PROFILE_FIELDS.map((f) => (
          <Field key={f.key} name={f.key} label={f.label} hint={f.hint === "" ? undefined : f.hint}>
            <Input
              value={values[f.key]}
              onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
              maxLength={f.key === "country" ? 64 : 255}
            />
          </Field>
        ))}
        <Field
          name="language"
          label="Language"
          hint="Formats dates and numbers to your preference. Full translations are coming later."
        >
          <Select
            options={languageOptions}
            value={values.language}
            onChange={(e) => setValues((v) => ({ ...v, language: e.target.value }))}
          />
        </Field>
      </Form>
    </Card>
  );
}

/* ------------------------------------------------------------ custom fields */

// A field's answer is present when a text field is non-empty or a checkbox has a value — the same
// rule the server's profile-complete gate applies.
function fieldAnswered(f: MeField): boolean {
  if (f.field_type === "boolean") return typeof f.value === "boolean";
  return typeof f.value === "string" && f.value.trim() !== "";
}

// A field is writable here when it is editable, or when it is required and not yet answered — the
// second clause is the remedy that lets a player clear a profile gate a new required field raised.
function fieldWritable(f: MeField): boolean {
  return f.editable || (f.required && !fieldAnswered(f));
}

/**
 * The custom registration fields, answerable from settings. This is the login-gate remedy: a
 * required field added after sign-up shows here, and answering it clears the wall that was
 * redirecting the player to this page.
 */
function CustomFields({ me }: { me: Me }) {
  const toast = useToast();
  const answer = useAnswerFields();
  const fields = me.fields ?? [];
  const [values, setValues] = useState<FieldValues>(() => initialFieldValues(fields));

  useEffect(() => {
    setValues(initialFieldValues(fields));
  }, [fields]);

  const writableIds = fields.filter(fieldWritable).map((f) => f.id);
  const owed = fields.some((f) => f.required && !fieldAnswered(f));

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    answer.mutate(
      { fields: answersFor(writableIds, values) },
      { onSuccess: () => toast.success("Answers saved") },
    );
  };

  return (
    <Card title="Registration fields">
      {owed && (
        <Alert tone="warn" title="A required field needs an answer">
          An organiser added a required field. Answer it below to reach the challenges.
        </Alert>
      )}
      <Form
        onSubmit={submit}
        error={answer.error ? messageOf(answer.error) : undefined}
        footer={
          <Button type="submit" variant="primary" loading={answer.isPending} disabled={writableIds.length === 0}>
            Save answers
          </Button>
        }
      >
        <CustomFieldInputs
          fields={fields}
          values={values}
          onChange={(id: number, value: FieldValue) => setValues((v) => ({ ...v, [id]: value }))}
          disabled={(f) => !fieldWritable(f)}
        />
      </Form>
    </Card>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="ff-row">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

/* ----------------------------------------------------------------- security */

function Security() {
  const toast = useToast();
  const change = useChangePassword();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    change.mutate(
      { current_password: current, new_password: next },
      {
        onSuccess: (sess) => {
          setCurrent("");
          setNext("");
          const revoked = sess.api_tokens_revoked ?? 0;
          toast.success(
            "Password changed",
            revoked > 0
              ? `Your other sessions were signed out and ${revoked} API token${
                  revoked === 1 ? "" : "s"
                } revoked. Mint replacements below.`
              : "Your other sessions were signed out.",
          );
        },
      },
    );
  };

  return (
    <Card title="Change password">
      {/* A 401 from this form is a wrong current password, not a dead session — the client
          knows not to bounce us to /login, and the message belongs on the field. */}
      <p className="ff-muted">
        This signs out your other sessions and revokes every API token on the account — the
        platform cannot tell routine rotation from a compromise, so it assumes the worse one.
      </p>
      <Form
        onSubmit={submit}
        error={change.error ? messageOf(change.error) : undefined}
        errors={fieldsOf(change.error)}
        footer={
          <Button type="submit" variant="primary" loading={change.isPending}>
            Change password
          </Button>
        }
      >
        <Field name="current_password" label="Current password" required>
          <Input
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            autoComplete="current-password"
            required
          />
        </Field>
        <Field name="new_password" label="New password" hint="At least 8 characters." required>
          <Input
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            autoComplete="new-password"
            minLength={8}
            maxLength={128}
            required
          />
        </Field>
      </Form>
    </Card>
  );
}

/* ------------------------------------------------------------------- tokens */

function Tokens() {
  const { data, isPending, error, refetch } = useQuery(tokensQuery);
  const [creating, setCreating] = useState(false);
  const [minted, setMinted] = useState<CreatedToken | null>(null);
  const [doomed, setDoomed] = useState<TokenListItem | null>(null);

  const create = useCreateToken();
  const remove = useDeleteToken();
  const toast = useToast();

  // Token routes are gated on a verified email like the challenges are, so the denial that
  // arrives here has a destination and PolicyGate follows it.
  if (error) return <LoadFailure error={error} onRetry={() => void refetch()} title="Could not load your tokens" />;

  const tokens = data?.tokens ?? [];

  const columns: readonly Column<TokenListItem>[] = [
    {
      key: "description",
      header: "Description",
      cell: (t) => t.description ?? <span className="muted">—</span>,
    },
    { key: "created", header: "Created", width: "10rem", cell: (t) => <RelativeTime value={t.created_at} /> },
    { key: "expires", header: "Expires", width: "10rem", cell: (t) => <RelativeTime value={t.expires_at} /> },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      width: "6rem",
      cell: (t) => (
        <Button variant="danger" size="sm" onClick={() => setDoomed(t)}>
          Revoke
        </Button>
      ),
    },
  ];

  return (
    <div className="ff-stack">
      <p className="muted">
        A token authenticates a script the way your session authenticates this page. It carries
        your account — and your bans — with it.
      </p>

      {remove.error && (
        <Alert tone="danger" title="Could not revoke that token">
          {messageOf(remove.error)}
        </Alert>
      )}

      <Card
        title="API tokens"
        flush
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            New token
          </Button>
        }
      >
        <DataTable
          caption="Your API tokens"
          columns={columns}
          rows={tokens}
          rowKey={(t) => t.id}
          loading={isPending}
          empty={
            <EmptyState
              title="No API tokens"
              description="Mint one to talk to the API from a script or a solver."
              action={
                <Button variant="primary" onClick={() => setCreating(true)}>
                  New token
                </Button>
              }
            />
          }
        />
      </Card>

      <CreateToken
        open={creating}
        onClose={() => setCreating(false)}
        mutation={create}
        onMinted={(token) => {
          setCreating(false);
          setMinted(token);
        }}
      />

      <RevealToken
        token={minted}
        onDismiss={() => {
          setMinted(null);
          // The plaintext also sits in the mutation's result until this is called; the reveal
          // is once, and once means it leaves the client's hands too.
          create.reset();
        }}
      />

      <ConfirmDestructive
        open={doomed !== null}
        onClose={() => setDoomed(null)}
        onConfirm={() => {
          if (doomed === null) return;
          remove.mutate(doomed.id, {
            onSuccess: () => {
              setDoomed(null);
              toast.success("Token revoked", "Anything using it stops working now.");
            },
            onError: () => setDoomed(null),
          });
        }}
        resourceName={doomed?.description ?? `token ${doomed?.id ?? ""}`}
        resourceKind="token"
        title="Revoke token"
        description="Every script still holding this token stops being able to authenticate."
        confirmLabel="Revoke token"
        busy={remove.isPending}
      />
    </div>
  );
}

function CreateToken({
  open,
  onClose,
  mutation,
  onMinted,
}: {
  open: boolean;
  onClose: () => void;
  mutation: ReturnType<typeof useCreateToken>;
  onMinted: (token: CreatedToken) => void;
}) {
  const [description, setDescription] = useState("");
  const [ttl, setTtl] = useState("720");

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const hours = Number(ttl);
    mutation.mutate(
      {
        description: description === "" ? undefined : description,
        ttl_hours: Number.isFinite(hours) && hours > 0 ? hours : undefined,
      },
      {
        onSuccess: (token) => {
          setDescription("");
          onMinted(token);
        },
      },
    );
  };

  return (
    <Dialog open={open} onClose={onClose} title="New API token" size="sm">
      <Form
        onSubmit={submit}
        error={mutation.error ? messageOf(mutation.error) : undefined}
        errors={fieldsOf(mutation.error)}
        footer={
          <>
            <Button variant="ghost" onClick={onClose} disabled={mutation.isPending}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={mutation.isPending}>
              Create token
            </Button>
          </>
        }
      >
        <Field name="description" label="Description" hint="What will use this token?">
          <Input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            maxLength={255}
            autoComplete="off"
          />
        </Field>
        <Field name="ttl_hours" label="Lifetime (hours)" hint="1 to 8760. Blank for the default.">
          <Input
            type="number"
            min={1}
            max={8760}
            value={ttl}
            onChange={(e) => setTtl(e.target.value)}
          />
        </Field>
      </Form>
    </Dialog>
  );
}

/**
 * The one and only sight of the plaintext.
 *
 * The server hashes it before it answers, so this dialog is the last moment it exists anywhere
 * a person can read it. Dismissing drops it from state — there is nothing to come back to.
 */
function RevealToken({ token, onDismiss }: { token: CreatedToken | null; onDismiss: () => void }) {
  if (token === null) return null;

  return (
    <Dialog
      open
      onClose={onDismiss}
      title="Your new API token"
      size="lg"
      closeOnBackdrop={false}
      footer={
        <Button variant="primary" onClick={onDismiss}>
          I have stored it
        </Button>
      }
    >
      <Alert tone="warn" title="This is shown once">
        Copy it now. It is stored hashed, so nobody — not you, not an organiser — can show it to
        you again. If you lose it, revoke it and mint another.
      </Alert>
      <CodeBlock code={token.token} title={token.description ?? "API token"} wrap />
      <p className="muted">
        Expires <RelativeTime value={token.expires_at} />. Send it as{" "}
        <span className="ff-mono">Authorization: Bearer …</span>.
      </p>
    </Dialog>
  );
}

/* --------------------------------------------------------------- error paths */

function messageOf(error: unknown): string {
  return isApiError(error) ? error.detail : "Something went wrong. Try again.";
}

/** A 422's per-field messages, keyed by the field name the form used. */
function fieldsOf(error: unknown): FieldErrors | undefined {
  if (!isApiError(error) || error.fieldErrors.length === 0) return undefined;
  return fieldErrors({ errors: error.fieldErrors });
}

function LoadFailure({
  error,
  onRetry,
  title,
}: {
  error: unknown;
  onRetry: () => void;
  title: string;
}) {
  if (denialOf(error) === null) {
    return (
      <Alert tone="danger" title={title}>
        <p>{messageOf(error)}</p>
        <Button onClick={onRetry}>Retry</Button>
      </Alert>
    );
  }
  return <PolicyGate error={error} />;
}
