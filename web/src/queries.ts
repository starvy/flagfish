// The query layer's public surface. Screens import from here; the split under `queries/` is an
// organisational detail, not a contract.
export { qk, ADMIN_STALE_TIME } from "./queries/keys";

export { instanceQuery } from "./queries/instance";

export {
  meQuery,
  useLogin,
  useRegister,
  useLogout,
  useUpdateMe,
  useChangePassword,
  useResetRequest,
  useResetApply,
  useVerifyResend,
  useVerifyConfirm,
} from "./queries/auth";

export {
  challengesQuery,
  challengeQuery,
  challengeSolvesQuery,
  // The name this query shipped under before it grew a sibling; kept so the board keeps building.
  challengeSolvesQuery as solvesQuery,
  useAttempt,
  useUnlockHint,
  useDownloadFile,
  type AttemptVars,
  type UnlockVars,
} from "./queries/challenges";

export { scoreboardQuery, bracketsQuery } from "./queries/scoreboard";

export {
  myTeamQuery,
  teamQuery,
  useCreateTeam,
  useJoinTeam,
  useLeaveTeam,
  useUpdateMyTeam,
  useKickMember,
  useTransferCaptaincy,
  useDisbandTeam,
} from "./queries/teams";

export { notificationsQuery } from "./queries/notifications";

export { tokensQuery, useCreateToken, useDeleteToken } from "./queries/tokens";

export { adminConfigQuery, useUpdateConfig } from "./queries/admin/config";

export {
  useCreateChallenge,
  useUpdateChallenge,
  useSetChallengeState,
  useSetChallengeRequirements,
  useAttachTag,
  useDetachTag,
  useReorderChallenges,
  useDeleteChallenge,
  useAddFlag,
  useUpdateFlag,
  useDeleteFlag,
  useAddHint,
  useUpdateHint,
  useDeleteHint,
  useUploadFile,
  useDeleteFile,
} from "./queries/admin/challenges";

export {
  adminUsersQuery,
  useSetUserBanned,
  useSetUserRole,
  useSetUserHidden,
  useUpdateUser,
  useForcePasswordChange,
  useAssignBracket,
} from "./queries/admin/users";

export {
  adminTeamsQuery,
  adminTeamQuery,
  useCreateAdminTeam,
  useUpdateAdminTeam,
  useSetTeamBanned,
  useSetTeamHidden,
} from "./queries/admin/teams";

export { adminTagsQuery, useMergeTag, useDeleteTag } from "./queries/admin/tags";

export {
  adminBracketsQuery,
  useCreateBracket,
  useUpdateBracket,
  useDeleteBracket,
} from "./queries/admin/brackets";

export { adminAwardsQuery, useGrantAward, useRevokeAward } from "./queries/admin/awards";

export {
  adminAuditQuery,
  flagSharingQuery,
  ipOverlapQuery,
  accountReportQuery,
  unissuedSolvesQuery,
  useCreateNotification,
} from "./queries/admin/moderation";
