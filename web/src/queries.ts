// The query layer's public surface. Screens import from here; the split under `queries/` is an
// organisational detail, not a contract.
export { qk, ADMIN_STALE_TIME } from "./queries/keys";

export { instanceQuery } from "./queries/instance";

export {
  meQuery,
  registrationFieldsQuery,
  useLogin,
  useRegister,
  useLogout,
  useUpdateMe,
  useAnswerFields,
  useChangeName,
  useChangeEmail,
  useConfirmEmailChange,
  useChangePassword,
  useResetRequest,
  useResetApply,
  useVerifyResend,
  useVerifyConfirm,
} from "./queries/auth";

export { userProfileQuery } from "./queries/profiles";

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

export { scoreboardQuery, bracketsQuery, scoreHistoryQuery } from "./queries/scoreboard";

export {
  myTeamQuery,
  teamQuery,
  useCreateTeam,
  useJoinTeam,
  useLeaveTeam,
  useUpdateMyTeam,
  useSetTeamJoinPassword,
  useKickMember,
  useTransferCaptaincy,
  useDisbandTeam,
} from "./queries/teams";

export { notificationsQuery } from "./queries/notifications";

export { tokensQuery, useCreateToken, useDeleteToken } from "./queries/tokens";

export { adminConfigQuery, useUpdateConfig } from "./queries/admin/config";

export {
  adminChallengesQuery,
  adminChallengeQuery,
  useCreateChallenge,
  useUpdateChallenge,
  useSetChallengeState,
  useSetChallengeRequirements,
  useAttachTag,
  useDetachTag,
  useSetAnnotation,
  useDeleteAnnotation,
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
  poolStatsQuery,
  challengeInstancesQuery,
  useUploadInstances,
  useSetChallengeFlagMode,
} from "./queries/admin/instances";

export {
  adminUsersQuery,
  useSetUserBanned,
  useSetUserRole,
  useSetUserHidden,
  useUpdateUser,
  useSetUserVerified,
  useVerifyAllUsers,
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
  adminTeamMembersQuery,
  useRemoveTeamMember,
  useMoveTeamMember,
} from "./queries/admin/teams";

export { adminTagsQuery, useMergeTag, useDeleteTag } from "./queries/admin/tags";

export {
  adminBracketsQuery,
  useCreateBracket,
  useUpdateBracket,
  useDeleteBracket,
} from "./queries/admin/brackets";

export {
  adminFieldsQuery,
  useCreateField,
  useUpdateField,
  useDeleteField,
} from "./queries/admin/fields";

export { adminAwardsQuery, useGrantAward, useRevokeAward } from "./queries/admin/awards";

export { pagesQuery, pageQuery } from "./queries/pages";

export {
  adminPagesQuery,
  adminPageQuery,
  useCreatePage,
  useUpdatePage,
  useDeletePage,
} from "./queries/admin/pages";

export {
  adminAuditQuery,
  flagSharingQuery,
  ipOverlapQuery,
  accountReportQuery,
  unissuedSolvesQuery,
  useCreateNotification,
} from "./queries/admin/moderation";

export { adminSubmissionsQuery, adminStatsQuery } from "./queries/admin/monitoring";
