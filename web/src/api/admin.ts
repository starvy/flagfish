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
export type AdminFlag = Schemas["AdminFlagBody"];
export type AdminHint = Schemas["AdminHintBody"];
export type AdminFile = Schemas["AdminFileBody"];
export type AdminTag = Schemas["AdminTag"];
export type AdminUser = Schemas["AdminUserBody"];
export type AdminBracket = Schemas["AdminBracketBody"];
export type AdminAuditEntry = Schemas["AdminAuditEntry"];
export type AdminNotification = Schemas["NotificationBody"];
export type AcSharingPair = Schemas["AcSharingPairBody"];
export type AcIPCluster = Schemas["AcIPClusterBody"];
export type AcAccountReport = Schemas["AcAccountReportOutputBody"];

export interface PageParams {
  page?: number;
  per_page?: number;
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

export const adminApi = {
  // Config
  getConfig: () => request<AdminConfig>("GET", `${P}/config`),

  updateConfig: (body: AdminConfigPatch) =>
    request<AdminConfig>("PATCH", `${P}/config`, body),

  // Challenges
  createChallenge: (body: Body<Schemas["AdminCreateChallengeInputBody"]>) =>
    request<AdminChallenge>("POST", `${P}/challenges`, body),

  updateChallenge: (id: number, body: Body<Schemas["AdminUpdateChallengeInputBody"]>) =>
    request<AdminChallenge>("PATCH", `${P}/challenges/${id}`, body),

  setChallengeState: (id: number, state: "visible" | "hidden") =>
    request<AdminChallenge>("PUT", `${P}/challenges/${id}/state`, { state }),

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

  mergeTag: (value: string, into: string) =>
    request<void>("POST", `${P}/tags/${encodeURIComponent(value)}/merge`, { into }),

  // A tag still attached to challenges is a 409 unless force says otherwise.
  deleteTag: (value: string, force = false) =>
    request<void>("DELETE", `${P}/tags/${encodeURIComponent(value)}${query({ force })}`),

  // Users
  listUsers: (params: PageParams = {}) =>
    request<Schemas["AdminListUsersOutputBody"]>("GET", `${P}/users${query({ ...params })}`),

  setUserBanned: (id: number, banned: boolean) =>
    request<Schemas["AdminBanOutputBody"]>("PUT", `${P}/users/${id}/ban`, { banned }),

  setUserRole: (id: number, role: "user" | "admin") =>
    request<Schemas["AdminRoleOutputBody"]>("PUT", `${P}/users/${id}/role`, { role }),

  // Brackets
  createBracket: (body: Body<Schemas["AdminCreateBracketInputBody"]>) =>
    request<AdminBracket>("POST", `${P}/brackets`, body),

  listBrackets: () => request<Schemas["AdminListBracketsOutputBody"]>("GET", `${P}/brackets`),

  updateBracket: (id: number, body: Body<Schemas["AdminUpdateBracketInputBody"]>) =>
    request<AdminBracket>("PATCH", `${P}/brackets/${id}`, body),

  deleteBracket: (id: number) => request<void>("DELETE", `${P}/brackets/${id}`),

  // An explicit null clears the assignment; the field is required, so it is always sent.
  assignBracket: (accountId: number, bracketId: number | null) =>
    request<Schemas["AdminAssignBracketOutputBody"]>(
      "PUT",
      `${P}/accounts/${accountId}/bracket`,
      { bracket_id: bracketId },
    ),

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
};
