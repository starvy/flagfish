import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { isApiError, type CreatedToken, type Me, type TokenListItem } from "../../api/client";
import {
  meQuery,
  tokensQuery,
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
          <Row label="Name">{me.name}</Row>
          <Row label="Email">
            <span className="ff-row">
              {me.email}
              {me.verified ? (
                <Badge tone="success">verified</Badge>
              ) : (
                <Badge tone="warn">not verified</Badge>
              )}
            </span>
          </Row>
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
        </dl>
        <p className="muted">
          Name and email are your identity here — an organiser can fix those if they are wrong.
        </p>
      </Card>

      <ProfileForm me={me} />

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

/** The player-owned fields. Submitting sends all three; an emptied box clears its field. */
function ProfileForm({ me }: { me: Me }) {
  const toast = useToast();
  const update = useUpdateMe();
  const [values, setValues] = useState<Record<string, string>>({
    website: me.website ?? "",
    affiliation: me.affiliation ?? "",
    country: me.country ?? "",
  });

  useEffect(() => {
    setValues({
      website: me.website ?? "",
      affiliation: me.affiliation ?? "",
      country: me.country ?? "",
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
      },
      { onSuccess: () => toast.success("Profile saved") },
    );
  };

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
        onSuccess: () => {
          setCurrent("");
          setNext("");
          toast.success("Password changed", "Your other sessions were signed out.");
        },
      },
    );
  };

  return (
    <Card title="Change password">
      {/* A 401 from this form is a wrong current password, not a dead session — the client
          knows not to bounce us to /login, and the message belongs on the field. */}
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
