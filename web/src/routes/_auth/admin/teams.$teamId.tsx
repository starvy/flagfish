import { useEffect, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import type { AdminTeam } from "../../../api/admin";
import { isApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import {
  adminTeamQuery,
  useSetTeamBanned,
  useSetTeamHidden,
  useUpdateAdminTeam,
} from "../../../queries";
import { Def, DefList, TriStateField, encodeText, triInvalid, triKeep, triPatch, type TriValue } from "../../../admin";
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDestructive,
  Dialog,
  Field,
  Form,
  Input,
  RelativeTime,
  Skeleton,
  useToast,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/teams/$teamId")({
  component: TeamDetailPage,
});

function TeamDetailPage() {
  const { teamId } = Route.useParams();
  const id = Number(teamId);
  const team = useQuery(adminTeamQuery(id));

  if (!Number.isInteger(id) || id <= 0) {
    return (
      <Alert tone="danger" title="Not a team">
        <Link to="/admin/teams">Back to the team list</Link>
      </Alert>
    );
  }

  if (team.isError) {
    const denial = denialOf(team.error);
    if (denial !== null) return <PolicyGate error={team.error} />;
    return (
      <Alert tone="danger" title="Could not load the team">
        <p>{messageOf(team.error)}</p>
        <Button size="sm" onClick={() => void team.refetch()}>
          Retry
        </Button>
      </Alert>
    );
  }

  if (team.isPending) {
    return (
      <Card>
        <Skeleton lines={6} />
      </Card>
    );
  }

  return <TeamDetail team={team.data} />;
}

function TeamDetail({ team }: { team: AdminTeam }) {
  const toast = useToast();

  return (
    <>
      <div className="page-head">
        <h1>{team.name}</h1>
        <span className="ff-row">
          {team.banned && <Badge tone="danger">banned</Badge>}
          {team.hidden && <Badge tone="neutral">hidden</Badge>}
          <Link to="/admin/teams" className="muted">
            ← all teams
          </Link>
        </span>
      </div>

      <div className="ff-stack">
        <Card title="Team">
          <DefList>
            <Def term="Id">
              <span className="ff-mono">{team.id}</span>
            </Def>
            <Def term="Members">{team.member_count}</Def>
            <Def term="Captain">
              {team.captain_id == null ? (
                <span className="muted">none — the first member to join adopts the seat</span>
              ) : (
                <span className="ff-mono">user {team.captain_id}</span>
              )}
            </Def>
            <Def term="Created">
              <RelativeTime value={team.created_at} />
            </Def>
          </DefList>
        </Card>

        <ProfileEditor team={team} onSaved={() => toast.success("Team saved")} />

        <ModerationControls team={team} />
      </div>
    </>
  );
}

/**
 * The tri-state PATCH form: each nullable field is an explicit keep | clear | set, so an
 * untouched field is provably an omitted key and an empty box can never silently clear.
 */
function ProfileEditor({ team, onSaved }: { team: AdminTeam; onSaved: () => void }) {
  const update = useUpdateAdminTeam();

  const [name, setName] = useState(team.name);
  const [email, setEmail] = useState<TriValue>(triKeep(team.email ?? ""));
  const [website, setWebsite] = useState<TriValue>(triKeep(team.website ?? ""));
  const [affiliation, setAffiliation] = useState<TriValue>(triKeep(team.affiliation ?? ""));
  const [country, setCountry] = useState<TriValue>(triKeep(team.country ?? ""));

  // A refetch (after save, after a ban) re-arms the form on the server's truth.
  useEffect(() => {
    setName(team.name);
    setEmail(triKeep(team.email ?? ""));
    setWebsite(triKeep(team.website ?? ""));
    setAffiliation(triKeep(team.affiliation ?? ""));
    setCountry(triKeep(team.country ?? ""));
  }, [team]);

  const invalid =
    triInvalid(email, encodeText) ||
    triInvalid(website, encodeText) ||
    triInvalid(affiliation, encodeText) ||
    triInvalid(country, encodeText);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (invalid) return;
    update.mutate(
      {
        id: team.id,
        body: {
          ...(name.trim() !== "" && name !== team.name ? { name: name.trim() } : {}),
          email: triPatch(email, encodeText),
          website: triPatch(website, encodeText),
          affiliation: triPatch(affiliation, encodeText),
          country: triPatch(country, encodeText),
        },
      },
      { onSuccess: onSaved },
    );
  };

  const text = (
    value: TriValue,
    onChange: (v: TriValue) => void,
    nameAttr: string,
    label: string,
    clearNote: string,
    current: string | null | undefined,
  ) => (
    <TriStateField
      name={nameAttr}
      label={label}
      value={value}
      onChange={onChange}
      current={current == null || current === "" ? "not set" : current}
      clearNote={clearNote}
    >
      {(control, v, onValue) => (
        <Input {...control} value={v} onChange={(e) => onValue(e.target.value)} maxLength={255} />
      )}
    </TriStateField>
  );

  return (
    <Card title="Profile">
      <Form
        onSubmit={submit}
        error={update.error ? messageOf(update.error) : undefined}
        footer={
          <Button type="submit" variant="primary" loading={update.isPending} disabled={invalid}>
            Save profile
          </Button>
        }
      >
        <Field name="name" label="Name" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={128} required />
        </Field>
        {text(email, setEmail, "email", "Contact email", "The team will have no contact address.", team.email)}
        {text(website, setWebsite, "website", "Website", "The team will list no website.", team.website)}
        {text(
          affiliation,
          setAffiliation,
          "affiliation",
          "Affiliation",
          "The team will list no affiliation.",
          team.affiliation,
        )}
        {text(country, setCountry, "country", "Country", "The team will list no country.", team.country)}
      </Form>
    </Card>
  );
}

const SELF_TEAM_BAN = "you cannot ban your own team";

function ModerationControls({ team }: { team: AdminTeam }) {
  const toast = useToast();
  const setBanned = useSetTeamBanned();
  const setHidden = useSetTeamHidden();
  const [confirmBan, setConfirmBan] = useState(false);
  const [confirmHide, setConfirmHide] = useState(false);
  const [banConflict, setBanConflict] = useState<string | null>(null);

  return (
    <Card title="Moderation">
      <p className="muted">
        Hiding takes the team off the public surfaces; members keep playing. A ban walls every
        member on every credential and kills their sessions. Stamped solves never move either way.
      </p>
      <div className="ff-row">
        <Button variant="secondary" onClick={() => setConfirmHide(true)}>
          {team.hidden ? "Unhide team" : "Hide team"}
        </Button>
        <Button
          variant={team.banned ? "secondary" : "danger"}
          onClick={() => {
            setBanConflict(null);
            setConfirmBan(true);
          }}
        >
          {team.banned ? "Unban team" : "Ban team"}
        </Button>
      </div>

      {confirmBan && (
        <ConfirmDestructive
          open
          onClose={() => setConfirmBan(false)}
          onConfirm={() =>
            setBanned.mutate(
              { id: team.id, banned: !team.banned },
              {
                onSuccess: (t) => {
                  setConfirmBan(false);
                  toast.success(t.banned ? `${t.name} is banned` : `${t.name} is unbanned`);
                },
                onError: (error) => {
                  if (isApiError(error) && error.status === 409) {
                    setBanConflict(SELF_TEAM_BAN);
                    return;
                  }
                  toast.error("Could not change the ban", messageOf(error));
                },
              },
            )
          }
          resourceName={team.name}
          resourceKind="team"
          title={team.banned ? "Unban team" : "Ban team"}
          confirmLabel={team.banned ? "Unban team" : "Ban team"}
          busy={setBanned.isPending}
          description={
            team.banned
              ? "Members can log in again and the team reappears on the scoreboard."
              : "Every member loses their sessions immediately and stays walled on every credential."
          }
        >
          {banConflict !== null && (
            <Alert tone="danger" title="Refused">
              {banConflict} — an admin must never ban the instance out from under itself.
            </Alert>
          )}
        </ConfirmDestructive>
      )}

      {confirmHide && (
        <Dialog
          open
          size="sm"
          onClose={() => setConfirmHide(false)}
          title={team.hidden ? "Unhide team" : "Hide team"}
          description={
            team.hidden
              ? `${team.name} reappears on the scoreboard and its public page comes back.`
              : `${team.name} disappears from the scoreboard and its public page 404s.`
          }
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirmHide(false)}>
                Cancel
              </Button>
              <Button
                variant="primary"
                loading={setHidden.isPending}
                onClick={() =>
                  setHidden.mutate(
                    { id: team.id, hidden: !team.hidden },
                    {
                      onSuccess: (t) => {
                        setConfirmHide(false);
                        toast.success(t.hidden ? `${t.name} is hidden` : `${t.name} is visible`);
                      },
                      onError: (error) => toast.error("Could not change visibility", messageOf(error)),
                    },
                  )
                }
              >
                {team.hidden ? "Unhide" : "Hide"}
              </Button>
            </>
          }
        />
      )}
    </Card>
  );
}

function messageOf(error: unknown): string {
  if (isApiError(error)) return error.detail;
  return error instanceof Error ? error.message : "unexpected error";
}
