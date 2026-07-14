import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { AdminConfig, AdminConfigPatch } from "../../../api/admin";
import { adminConfigQuery, instanceQuery, useUpdateConfig } from "../../../queries";
import { DEFAULT_THEME, THEMES, themeByName } from "../../../theme/registry";
import { sanitizeOverrides, type TokenOverrides } from "../../../theme/tokens";
import {
  AdminPage,
  ErrorState,
  LoadingState,
  ThemePreview,
  TokenEditor,
  TriStateField,
  fieldErrorsOf,
  formErrorOf,
  isoToLocalInput,
  localInputToIso,
  localZone,
  mergeTokens,
  triInvalid,
  triKeep,
  triPatch,
  type TriValue,
} from "../../../admin";
import {
  Alert,
  Badge,
  Button,
  Card,
  Field,
  Form,
  Input,
  Select,
  Textarea,
  useToast,
  type FieldErrors,
  type SelectOption,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/config")({
  loader: ({ context }) => context.queryClient.ensureQueryData(adminConfigQuery),
  component: ConfigPage,
});

const CHALLENGE_VIS = ["public", "private"] as const;
const SCORE_VIS = ["public", "private", "hidden"] as const;
const ACCOUNT_VIS = ["public", "private"] as const;
const REGISTRATION_VIS = ["public", "private", "mlc"] as const;

function ConfigPage() {
  const config = useQuery(adminConfigQuery);
  const instance = useQuery(instanceQuery);
  // The saved state is the only state worth rendering, so a successful write remounts the form
  // against what came back rather than leaving the operator's draft on screen pretending.
  const [savedAt, setSavedAt] = useState(0);

  if (config.error) {
    return (
      <Page>
        <ErrorState error={config.error} onRetry={() => void config.refetch()} />
      </Page>
    );
  }
  if (!config.data) {
    return (
      <Page>
        <LoadingState rows={8} />
      </Page>
    );
  }

  return (
    <Page>
      <ConfigForm
        key={savedAt}
        config={config.data}
        mode={instance.data?.mode}
        onSaved={() => setSavedAt(Date.now())}
      />
    </Page>
  );
}

function Page({ children }: { children: ReactNode }) {
  return (
    <AdminPage
      title="Config"
      description="Event identity, the clock, the theme and who may see what. Every write is the whole form: the server merges it, validates it as one, and refuses it whole if the result is incoherent."
    >
      {children}
    </AdminPage>
  );
}

interface Draft {
  name: string;
  description: string;
  theme: string;
  overrides: TokenOverrides;
  start: TriValue;
  end: TriValue;
  freeze: TriValue;
  challenge_visibility: string;
  score_visibility: string;
  account_visibility: string;
  registration_visibility: string;
}

/**
 * Reads the stored override blob. It is a JSON *string* on this endpoint, and a broken one is
 * not an empty one: telling the operator the instance is running unstyled when it is actually
 * running with tokens we failed to parse would be the silent wrong answer.
 */
function readOverrides(raw: string | undefined): { overrides: TokenOverrides; broken: boolean } {
  if (!raw || raw.trim() === "") return { overrides: {}, broken: false };
  try {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      return { overrides: {}, broken: true };
    }
    return { overrides: sanitizeOverrides(parsed as Record<string, unknown>), broken: false };
  } catch {
    return { overrides: {}, broken: true };
  }
}

function sameOverrides(a: TokenOverrides, b: TokenOverrides): boolean {
  const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
  for (const k of keys) {
    if (a[k as keyof TokenOverrides] !== b[k as keyof TokenOverrides]) return false;
  }
  return true;
}

/**
 * Options for an enum the server hands back as a bare string. If it names something this build
 * does not know — the policy layer has visibilities the config endpoint will not accept back —
 * it is still offered, so the form can leave it alone instead of quietly rewriting it.
 */
function visOptions(current: string, allowed: readonly string[]): SelectOption[] {
  const known = allowed.map((value) => ({ value, label: value }));
  if (allowed.includes(current)) return known;
  return [{ value: current, label: `${current} (set outside this form)` }, ...known];
}

interface ConfigFormProps {
  config: AdminConfig;
  mode: string | undefined;
  onSaved: () => void;
}

function ConfigForm({ config, mode, onSaved }: ConfigFormProps) {
  const toast = useToast();
  const update = useUpdateConfig();
  const stored = useMemo(() => readOverrides(config.theme_tokens), [config.theme_tokens]);

  const [draft, setDraft] = useState<Draft>({
    name: config.name,
    description: config.description,
    theme: config.theme,
    overrides: stored.overrides,
    start: triKeep(isoToLocalInput(config.start)),
    end: triKeep(isoToLocalInput(config.end)),
    freeze: triKeep(isoToLocalInput(config.freeze)),
    challenge_visibility: config.challenge_visibility,
    score_visibility: config.score_visibility,
    account_visibility: config.account_visibility,
    registration_visibility: config.registration_visibility,
  });
  const [clockErrors, setClockErrors] = useState<FieldErrors>({});

  const set = <K extends keyof Draft>(key: K, value: Draft[K]) =>
    setDraft((d) => ({ ...d, [key]: value }));

  const base = themeByName(draft.theme)?.tokens ?? DEFAULT_THEME.tokens;
  const previewTokens = mergeTokens(base, draft.overrides);
  const themeLabel = themeByName(draft.theme)?.label ?? draft.theme;

  const overridesChanged = !sameOverrides(draft.overrides, stored.overrides);
  const dirty =
    draft.name !== config.name ||
    draft.description !== config.description ||
    draft.theme !== config.theme ||
    overridesChanged ||
    draft.challenge_visibility !== config.challenge_visibility ||
    draft.score_visibility !== config.score_visibility ||
    draft.account_visibility !== config.account_visibility ||
    draft.registration_visibility !== config.registration_visibility ||
    draft.start.mode !== "keep" ||
    draft.end.mode !== "keep" ||
    draft.freeze.mode !== "keep";

  const serverErrors = fieldErrorsOf(update.error);
  const errors: FieldErrors = { ...serverErrors, ...clockErrors };

  const onSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();

    // A `set` with nothing usable in it is a validation failure, never a clear: sending null
    // here would wipe a field the operator was in the middle of typing.
    const bad: FieldErrors = {};
    for (const key of ["start", "end", "freeze"] as const) {
      if (triInvalid(draft[key], localInputToIso)) {
        bad[key] = "give a date and time, or choose keep or clear";
      }
    }
    setClockErrors(bad);
    if (Object.keys(bad).length > 0) return;

    const patch: AdminConfigPatch = {
      start: triPatch(draft.start, localInputToIso),
      end: triPatch(draft.end, localInputToIso),
      freeze: triPatch(draft.freeze, localInputToIso),
    };
    if (draft.name !== config.name) patch.name = draft.name;
    if (draft.description !== config.description) patch.description = draft.description;
    if (draft.theme !== config.theme) patch.theme = draft.theme;
    if (overridesChanged) patch.theme_tokens = JSON.stringify(draft.overrides);
    if (draft.challenge_visibility !== config.challenge_visibility) {
      patch.challenge_visibility = draft.challenge_visibility as AdminConfigPatch["challenge_visibility"];
    }
    if (draft.score_visibility !== config.score_visibility) {
      patch.score_visibility = draft.score_visibility as AdminConfigPatch["score_visibility"];
    }
    if (draft.account_visibility !== config.account_visibility) {
      patch.account_visibility = draft.account_visibility as AdminConfigPatch["account_visibility"];
    }
    if (draft.registration_visibility !== config.registration_visibility) {
      patch.registration_visibility =
        draft.registration_visibility as AdminConfigPatch["registration_visibility"];
    }

    update.mutate(patch, {
      onSuccess: (saved) => {
        toast.success("Config saved", `The instance is now “${saved.name}”.`);
        onSaved();
      },
      onError: (error) => toast.error("Could not save the config", formErrorOf(error)),
    });
  };

  return (
    <Form
      onSubmit={onSubmit}
      errors={errors}
      error={formErrorOf(update.error)}
      footer={
        <>
          <Button type="submit" loading={update.isPending} disabled={!dirty}>
            Save
          </Button>
          <span className="ff-spacer" />
          {!dirty && <span className="ff-muted">No changes.</span>}
        </>
      }
    >
      <Card title="Identity">
        <Field name="name" label="Event name" required>
          <Input value={draft.name} onChange={(e) => set("name", e.currentTarget.value)} />
        </Field>
        <Field
          name="description"
          label="Description"
          hint="Shown on the landing page. Markdown is rendered where it is displayed."
        >
          <Textarea
            rows={4}
            value={draft.description}
            onChange={(e) => set("description", e.currentTarget.value)}
          />
        </Field>
      </Card>

      <Card
        title="The clock"
        actions={<Badge tone="neutral">{localZone()}</Badge>}
        footer={
          <span className="ff-muted">
            Times are entered in your local zone and stored in UTC. The server refuses an
            incoherent clock — a freeze after the end, an end before the start — as one write,
            so nothing lands half-applied.
          </span>
        }
      >
        <Alert tone="info" title="Keep, clear, or set — never by accident">
          An untouched field is not sent at all. <strong>Clear</strong> removes the time from the
          event; <strong>set</strong> replaces it. There is no way to erase a time by emptying a
          box, which is exactly the point.
        </Alert>

        <ClockField
          name="start"
          label="Start"
          value={draft.start}
          current={config.start}
          clearNote="Cleared — the CTF is open from the moment it exists."
          onChange={(v) => set("start", v)}
        />
        <ClockField
          name="end"
          label="End"
          value={draft.end}
          current={config.end}
          clearNote="Cleared — the CTF never closes."
          onChange={(v) => set("end", v)}
        />
        <ClockField
          name="freeze"
          label="Freeze"
          value={draft.freeze}
          current={config.freeze}
          clearNote="Cleared — the board stays live to the last second."
          onChange={(v) => set("freeze", v)}
        />
      </Card>

      <Card title="Theme">
        {stored.broken && (
          <Alert tone="danger" title="The stored token overrides are not valid JSON">
            This form cannot show what the instance is actually running. Saving the theme section
            will replace the stored blob with what you see below.
          </Alert>
        )}

        <Field
          name="theme"
          label="Default theme"
          hint="The theme a player gets before they pick one of their own."
        >
          <Select
            value={draft.theme}
            onChange={(e) => set("theme", e.currentTarget.value)}
            options={themeOptions(config.theme)}
          />
        </Field>

        <Field
          name="theme_tokens"
          label="Token overrides"
          hint="Each key is one variable in the token contract. An empty box is not an override — the theme's own value stands."
        >
          {() => (
            <div className="admin-grid">
              <TokenEditor
                base={base}
                overrides={draft.overrides}
                onChange={(next) => set("overrides", next)}
                disabled={update.isPending}
              />
              <ThemePreview tokens={previewTokens} themeLabel={themeLabel} />
            </div>
          )}
        </Field>
      </Card>

      <Card title="Visibility">
        <Field
          name="challenge_visibility"
          label="Challenges"
          hint="private: only a logged-in player sees the board."
        >
          <Select
            value={draft.challenge_visibility}
            onChange={(e) => set("challenge_visibility", e.currentTarget.value)}
            options={visOptions(draft.challenge_visibility, CHALLENGE_VIS)}
          />
        </Field>
        <Field
          name="score_visibility"
          label="Scores"
          hint="hidden: the scoreboard is off for everyone but an admin."
        >
          <Select
            value={draft.score_visibility}
            onChange={(e) => set("score_visibility", e.currentTarget.value)}
            options={visOptions(draft.score_visibility, SCORE_VIS)}
          />
        </Field>
        <Field name="account_visibility" label="Accounts">
          <Select
            value={draft.account_visibility}
            onChange={(e) => set("account_visibility", e.currentTarget.value)}
            options={visOptions(draft.account_visibility, ACCOUNT_VIS)}
          />
        </Field>
        <Field
          name="registration_visibility"
          label="Registration"
          hint="private and mlc both make the register form a 404 — it does not exist, rather than refusing."
        >
          <Select
            value={draft.registration_visibility}
            onChange={(e) => set("registration_visibility", e.currentTarget.value)}
            options={visOptions(draft.registration_visibility, REGISTRATION_VIS)}
          />
        </Field>
      </Card>

      <Card title="Account mode">
        <p>
          This instance runs in <Badge tone="accent">{mode ?? "unknown"}</Badge> mode.
        </p>
        <p className="ff-muted">
          <strong>Mode is not editable here, and not editable at all.</strong> It is fixed when the
          instance is set up: every solve, every score and every bracket is attributed to an
          account of that kind, and changing it under a running event would orphan all of them.
          A different mode is a different instance.
        </p>
      </Card>
    </Form>
  );
}

function themeOptions(current: string): SelectOption[] {
  const known = THEMES.map((t) => ({ value: t.name, label: t.label }));
  if (THEMES.some((t) => t.name === current)) return known;
  // A theme name this build does not ship: players fall back, but the operator should see it.
  return [{ value: current, label: `${current} (not shipped in this build)` }, ...known];
}

interface ClockFieldProps {
  name: "start" | "end" | "freeze";
  label: string;
  value: TriValue;
  current: string | undefined;
  clearNote: string;
  onChange: (next: TriValue) => void;
}

function ClockField({ name, label, value, current, clearNote, onChange }: ClockFieldProps) {
  return (
    <TriStateField
      name={name}
      label={label}
      value={value}
      onChange={onChange}
      current={
        current ? (
          <span className="ff-mono">{new Date(current).toLocaleString()}</span>
        ) : (
          <span className="muted">not set</span>
        )
      }
      clearNote={clearNote}
    >
      {(control, raw, onValue) => (
        <Input
          {...control}
          type="datetime-local"
          value={raw}
          onChange={(e) => onValue(e.currentTarget.value)}
        />
      )}
    </TriStateField>
  );
}
