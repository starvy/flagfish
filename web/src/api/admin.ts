import type { components } from "./schema.admin.gen";
import { query, request, upload } from "./client";

type Schemas = components["schemas"];

/** A request body as the caller writes it: the generated schema minus Huma's `$schema` marker. */
type Body<T> = Omit<T, "$schema">;

// The admin surface is the same session, the same cookie, the same CSRF token and the same
// 401 rule as the public one — it is a path prefix, not a second auth story.
const P = "/admin";

export type AdminConfig = Schemas["AdminConfigOutputBody"];
export type AdminConfigPatch = Body<Schemas["AdminConfigInputBody"]>;
export type AdminChallenge = Schemas["AdminChallengeBody"];
export type AdminChallengeListItem = Schemas["AdminChallengeListItem"];
export type AdminChallengeDetail = Schemas["AdminChallengeDetailOutputBody"];
export type AdminRequirements = Schemas["AdminRequirementsBody"];
export type AdminSetRequirementsResult = Schemas["AdminSetRequirementsOutputBody"];
export type AdminChallengeTag = Schemas["AdminChallengeTagBody"];
export type AdminFlag = Schemas["AdminFlagBody"];
export type AdminHint = Schemas["AdminHintBody"];
export type AdminFile = Schemas["AdminFileBody"];
export type AdminTag = Schemas["AdminTag"];
export type AdminUser = Schemas["AdminUserBody"];
export type AdminTeam = Schemas["AdminTeamBody"];
export type AdminTeamMember = Schemas["AdminTeamMemberBody"];
export type AdminBracket = Schemas["AdminBracketBody"];
export type AdminField = Schemas["AdminFieldBody"];
export type AdminAward = Schemas["AdminAwardBody"];
export type AdminPage = Schemas["AdminPageBody"];
export type AdminAuditEntry = Schemas["AdminAuditEntry"];
export type AdminNotification = Schemas["NotificationBody"];
export type AcSharingPair = Schemas["AcSharingPairBody"];
export type AcIPCluster = Schemas["AcIPClusterBody"];
export type AcAccountReport = Schemas["AcAccountReportOutputBody"];
export type AdminInstance = Schemas["AdminInstanceBody"];
export type AdminTask = Schemas["TaskBody"];
export type PoolStat = Schemas["PoolStatBody"];
export type AdminPoolUploadResult = Schemas["AdminPoolUploadOutputBody"];

export interface PageParams {
  page?: number;
  per_page?: number;
}

/** One (q, field) search over a paginated list. field defaults to name server-side. */
export interface SearchParams extends PageParams {
  q?: string;
  field?: "name" | "email" | "website" | "affiliation" | "country";
}

export interface AuditParams extends PageParams {
  actor?: number;
  action?: "INSERT" | "UPDATE" | "DELETE";
  target_table?: string;
  target_id?: number;
}

export interface IPOverlapParams extends PageParams {
  min_accounts?: number;
}

export type AdminSubmission = Schemas["SubmissionBody"];
export type AdminStats = Schemas["AdminStatsOutputBody"];

/** The verdict a submission row carries; a bad value is a 422 the screen surfaces. */
export type SubmissionType = "correct" | "incorrect" | "partial" | "discard" | "ratelimited";
export type StatsBucket = "hour" | "day" | "week" | "month";

/** Keyset, not offset: `cursor` is the opaque `next_cursor` handed back, never parsed. */
export interface SubmissionsParams {
  type?: SubmissionType;
  cursor?: string;
  challenge_id?: number;
  user_id?: number;
  team_id?: number;
  limit?: number;
}

export interface StatsParams {
  bucket?: StatsBucket;
}

export const adminApi = {
  // Config
  getConfig: () => request<AdminConfig>("GET", `${P}/config`),

  updateConfig: (body: AdminConfigPatch) =>
    request<AdminConfig>("PATCH", `${P}/config`, body),

  // Challenges. The admin board and detail are their own reads — hidden challenges included, and
  // the detail returns flags, which no player-reachable route ever does.
  listChallenges: () =>
    request<Schemas["AdminListChallengesOutputBody"]>("GET", `${P}/challenges`),

  getChallenge: (id: number) =>
    request<AdminChallengeDetail>("GET", `${P}/challenges/${id}`),

  createChallenge: (body: Body<Schemas["AdminCreateChallengeInputBody"]>) =>
    request<AdminChallenge>("POST", `${P}/challenges`, body),

  updateChallenge: (id: number, body: Body<Schemas["AdminUpdateChallengeInputBody"]>) =>
    request<AdminChallenge>("PATCH", `${P}/challenges/${id}`, body),

  setChallengeState: (id: number, state: "visible" | "hidden") =>
    request<AdminChallenge>("PUT", `${P}/challenges/${id}/state`, { state }),

  // The guarded static ↔ unique switch: the server refuses it while the challenge has solves, has a
  // regex flag (→ unique), or has no flags (→ static), and surfaces those as a 422.
  setChallengeFlagMode: (id: number, flagMode: "static" | "unique") =>
    request<AdminChallenge>("PUT", `${P}/challenges/${id}/flag-mode`, { flag_mode: flagMode }),

  // Unique-flag instance pool. The client uploads sha256(flag) hashes — never the plaintext.
  poolStats: () => request<Schemas["AdminPoolStatsOutputBody"]>("GET", `${P}/pool/stats`),

  listInstances: (challengeId: number, params: PageParams = {}) =>
    request<Schemas["AdminListInstancesOutputBody"]>(
      "GET",
      `${P}/challenges/${challengeId}/instances${query({ ...params })}`,
    ),

  uploadInstances: (challengeId: number, body: Body<Schemas["AdminPoolUploadInputBody"]>) =>
    request<AdminPoolUploadResult>("PUT", `${P}/challenges/${challengeId}/instances`, body),

  // Whole-value replace: what you send is the entire prerequisite set.
  setChallengeRequirements: (id: number, body: Body<Schemas["AdminSetRequirementsInputBody"]>) =>
    request<AdminSetRequirementsResult>("PUT", `${P}/challenges/${id}/requirements`, body),

  reorderChallenges: (items: ReadonlyArray<{ id: number; position: number }>) =>
    request<Schemas["AdminReorderOutputBody"]>("PUT", `${P}/challenges/order`, { items }),

  deleteChallenge: (id: number) => request<void>("DELETE", `${P}/challenges/${id}`),

  // Flags
  addFlag: (challengeId: number, body: Body<Schemas["AdminAddFlagInputBody"]>) =>
    request<AdminFlag>("POST", `${P}/challenges/${challengeId}/flags`, body),

  updateFlag: (
    challengeId: number,
    flagId: number,
    body: Body<Schemas["AdminUpdateFlagInputBody"]>,
  ) => request<AdminFlag>("PATCH", `${P}/challenges/${challengeId}/flags/${flagId}`, body),

  deleteFlag: (challengeId: number, flagId: number) =>
    request<void>("DELETE", `${P}/challenges/${challengeId}/flags/${flagId}`),

  // Hints
  addHint: (challengeId: number, body: Body<Schemas["AdminAddHintInputBody"]>) =>
    request<AdminHint>("POST", `${P}/challenges/${challengeId}/hints`, body),

  updateHint: (
    challengeId: number,
    hintId: number,
    body: Body<Schemas["AdminUpdateHintInputBody"]>,
  ) => request<AdminHint>("PATCH", `${P}/challenges/${challengeId}/hints/${hintId}`, body),

  deleteHint: (challengeId: number, hintId: number) =>
    request<void>("DELETE", `${P}/challenges/${challengeId}/hints/${hintId}`),

  // Files. The server reads exactly one part, named `file`; the challenge comes from the path.
  uploadFile: (challengeId: number, file: File) => {
    const form = new FormData();
    form.append("file", file);
    return upload<AdminFile>("POST", `${P}/challenges/${challengeId}/files`, form);
  },

  deleteFile: (fileId: number) => request<void>("DELETE", `${P}/files/${fileId}`),

  // Tags
  listTags: () => request<Schemas["AdminListTagsOutputBody"]>("GET", `${P}/tags`),

  // A duplicate attach is a 409; a missing challenge a 404 — both straight from the constraints.
  attachTag: (challengeId: number, value: string) =>
    request<AdminChallengeTag>("POST", `${P}/challenges/${challengeId}/tags`, { value }),

  detachTag: (challengeId: number, value: string) =>
    request<void>("DELETE", `${P}/challenges/${challengeId}/tags/${encodeURIComponent(value)}`),

  mergeTag: (value: string, into: string) =>
    request<void>("POST", `${P}/tags/${encodeURIComponent(value)}/merge`, { into }),

  // A tag still attached to challenges is a 409 unless force says otherwise.
  deleteTag: (value: string, force = false) =>
    request<void>("DELETE", `${P}/tags/${encodeURIComponent(value)}${query({ force })}`),

  // Users
  listUsers: (params: SearchParams = {}) =>
    request<Schemas["AdminListUsersOutputBody"]>("GET", `${P}/users${query({ ...params })}`),

  getUser: (id: number) => request<AdminUser>("GET", `${P}/users/${id}`),

  updateUser: (id: number, body: Body<Schemas["AdminUpdateUserInputBody"]>) =>
    request<AdminUser>("PATCH", `${P}/users/${id}`, body),

  setUserBanned: (id: number, banned: boolean) =>
    request<Schemas["AdminBanOutputBody"]>("PUT", `${P}/users/${id}/ban`, { banned }),

  setUserHidden: (id: number, hidden: boolean) =>
    request<Schemas["AdminUserHiddenOutputBody"]>("PUT", `${P}/users/${id}/hidden`, { hidden }),

  setUserRole: (id: number, role: "user" | "admin") =>
    request<Schemas["AdminRoleOutputBody"]>("PUT", `${P}/users/${id}/role`, { role }),

  // The recovery for a mailer that is configured but broken: verification is a claim about
  // an address, and an organiser can make it on a player's behalf when no mail arrives.
  setUserVerified: (id: number, verified: boolean) =>
    request<Schemas["AdminVerifiedOutputBody"]>("PUT", `${P}/users/${id}/verified`, { verified }),

  verifyAllUsers: () =>
    request<Schemas["AdminVerifyAllOutputBody"]>("POST", `${P}/users/verify-all`),

  // Sets the flag and kills the user's sessions; they must pick a new password to play again.
  forcePasswordChange: (id: number) =>
    request<Schemas["AdminForcePasswordChangeOutputBody"]>(
      "PUT",
      `${P}/users/${id}/force-password-change`,
    ),

  // Teams
  listTeams: (params: SearchParams = {}) =>
    request<Schemas["AdminListTeamsOutputBody"]>("GET", `${P}/teams${query({ ...params })}`),

  getTeam: (id: number) => request<AdminTeam>("GET", `${P}/teams/${id}`),

  createTeam: (body: Body<Schemas["AdminCreateTeamInputBody"]>) =>
    request<AdminTeam>("POST", `${P}/teams`, body),

  updateTeam: (id: number, body: Body<Schemas["AdminUpdateTeamInputBody"]>) =>
    request<AdminTeam>("PATCH", `${P}/teams/${id}`, body),

  setTeamBanned: (id: number, banned: boolean) =>
    request<Schemas["AdminTeamBanOutputBody"]>("PUT", `${P}/teams/${id}/ban`, { banned }),

  setTeamHidden: (id: number, hidden: boolean) =>
    request<Schemas["AdminTeamHiddenOutputBody"]>("PUT", `${P}/teams/${id}/hidden`, { hidden }),

  // Team roster. Removing or moving a player re-points who they score for next; the ledger they
  // already wrote keeps the team it was stamped with, so no score moves.
  listTeamMembers: (id: number) =>
    request<Schemas["AdminListTeamMembersOutputBody"]>("GET", `${P}/teams/${id}/members`),

  removeTeamMember: (id: number, userId: number) =>
    request<void>("DELETE", `${P}/teams/${id}/members/${userId}`),

  moveTeamMember: (id: number, userId: number, toTeamId: number) =>
    request<void>("POST", `${P}/teams/${id}/members/${userId}/move`, { to_team_id: toTeamId }),

  // Brackets
  createBracket: (body: Body<Schemas["AdminCreateBracketInputBody"]>) =>
    request<AdminBracket>("POST", `${P}/brackets`, body),

  listBrackets: () => request<Schemas["AdminListBracketsOutputBody"]>("GET", `${P}/brackets`),

  updateBracket: (id: number, body: Body<Schemas["AdminUpdateBracketInputBody"]>) =>
    request<AdminBracket>("PATCH", `${P}/brackets/${id}`, body),

  deleteBracket: (id: number) => request<void>("DELETE", `${P}/brackets/${id}`),

  // Custom registration fields
  createField: (body: Body<Schemas["AdminCreateFieldInputBody"]>) =>
    request<AdminField>("POST", `${P}/fields`, body),

  listFields: () => request<Schemas["AdminListFieldsOutputBody"]>("GET", `${P}/fields`),

  updateField: (id: number, body: Body<Schemas["AdminUpdateFieldInputBody"]>) =>
    request<AdminField>("PATCH", `${P}/fields/${id}`, body),

  deleteField: (id: number) => request<void>("DELETE", `${P}/fields/${id}`),

  // An explicit null clears the assignment; the field is required, so it is always sent.
  assignBracket: (accountId: number, bracketId: number | null) =>
    request<Schemas["AdminAssignBracketOutputBody"]>(
      "PUT",
      `${P}/accounts/${accountId}/bracket`,
      { bracket_id: bracketId },
    ),

  // Manual awards (out-of-band point adjustments). account_id is the scoring account — a team in
  // teams mode, a user in users mode — resolved server-side from the instance's account model.
  listAwards: (accountId: number) =>
    request<Schemas["AdminListAwardsOutputBody"]>(
      "GET",
      `${P}/awards${query({ account_id: accountId })}`,
    ),

  grantAward: (body: Body<Schemas["AdminGrantAwardInputBody"]>) =>
    request<AdminAward>("POST", `${P}/awards`, body),

  revokeAward: (id: number) => request<void>("DELETE", `${P}/awards/${id}`),

  // Pages (the CMS behind rules/FAQ/sponsors). The admin surface sees drafts; the public one never
  // does. A duplicate route is a 409 straight from the table's unique index.
  listPages: () => request<Schemas["AdminListPagesOutputBody"]>("GET", `${P}/pages`),

  getPage: (id: number) => request<AdminPage>("GET", `${P}/pages/${id}`),

  createPage: (body: Body<Schemas["AdminCreatePageInputBody"]>) =>
    request<AdminPage>("POST", `${P}/pages`, body),

  updatePage: (id: number, body: Body<Schemas["AdminUpdatePageInputBody"]>) =>
    request<AdminPage>("PATCH", `${P}/pages/${id}`, body),

  deletePage: (id: number) => request<void>("DELETE", `${P}/pages/${id}`),

  // Notifications
  createNotification: (body: Body<Schemas["AdminCreateNotificationInputBody"]>) =>
    request<AdminNotification>("POST", `${P}/notifications`, body),

  // Audit
  listAudit: (params: AuditParams = {}) =>
    request<Schemas["AdminListAuditOutputBody"]>("GET", `${P}/audit${query({ ...params })}`),

  // Anti-cheat
  flagSharing: (params: PageParams = {}) =>
    request<Schemas["AcFlagSharingOutputBody"]>(
      "GET",
      `${P}/anticheat/flag-sharing${query({ ...params })}`,
    ),

  ipOverlap: (params: IPOverlapParams = {}) =>
    request<Schemas["AcIPOverlapOutputBody"]>(
      "GET",
      `${P}/anticheat/ip-overlap${query({ ...params })}`,
    ),

  accountReport: (id: number) =>
    request<AcAccountReport>("GET", `${P}/anticheat/accounts/${id}`),

  unissuedSolves: (params: PageParams = {}) =>
    request<Schemas["AcUnissuedSolvesOutputBody"]>(
      "GET",
      `${P}/anticheat/unissued-solves${query({ ...params })}`,
    ),

  // Read-only monitoring. The submissions log is keyset-paginated: pass the response's
  // `next_cursor` straight back as `cursor` for the next page, and treat it as opaque.
  submissions: (params: SubmissionsParams = {}) =>
    request<Schemas["AdminSubmissionsOutputBody"]>(
      "GET",
      `${P}/submissions${query({ ...params })}`,
    ),

  stats: (params: StatsParams = {}) =>
    request<Schemas["AdminStatsOutputBody"]>("GET", `${P}/stats${query({ ...params })}`),

  // Async ops (backup / restore / import). Each mutating call returns a task immediately; poll
  // getTask until it succeeds or fails. The download URL for a finished backup is carried on the
  // task itself (task.download), served by the same admin session cookie.
  startBackup: (profile: "backup" | "safe" = "backup") =>
    request<AdminTask>("POST", `${P}/backup`, { profile }),

  startRestore: (archive: File) => {
    const form = new FormData();
    form.append("archive", archive);
    return upload<AdminTask>("POST", `${P}/restore`, form);
  },

  startImport: (archive: File) => {
    const form = new FormData();
    form.append("archive", archive);
    return upload<AdminTask>("POST", `${P}/import`, form);
  },

  getTask: (id: number) => request<AdminTask>("GET", `${P}/tasks/${id}`),
};
