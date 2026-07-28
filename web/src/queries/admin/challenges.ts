import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

// The operator's board: every challenge, hidden included, with the flag/hint counts a player is
// never told. Keyed under the challenges prefix so a challenge write invalidates it by prefix.
export const adminChallengesQuery = queryOptions({
  queryKey: qk.adminChallenges(),
  queryFn: () => adminApi.listChallenges(),
  staleTime: ADMIN_STALE_TIME,
});

// One challenge with its flags, hints, tags and files — the editor's read. Also under the
// challenges prefix, so the same write hooks below drop it.
export const adminChallengeQuery = (id: number) =>
  queryOptions({
    queryKey: qk.adminChallenge(id),
    queryFn: () => adminApi.getChallenge(id),
    staleTime: ADMIN_STALE_TIME,
  });

// Every admin write to a challenge also changes what players see and what the editor reads, so the
// whole challenges subtree — both boards and every detail — is dropped.
function useChallengeWrite<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.challenges() });
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
    },
  });
}

export function useCreateChallenge() {
  return useChallengeWrite(adminApi.createChallenge);
}

export function useUpdateChallenge() {
  return useChallengeWrite((v: { id: number; body: Parameters<typeof adminApi.updateChallenge>[1] }) =>
    adminApi.updateChallenge(v.id, v.body),
  );
}

export function useSetChallengeState() {
  return useChallengeWrite((v: { id: number; state: "visible" | "hidden" }) =>
    adminApi.setChallengeState(v.id, v.state),
  );
}

export function useSetChallengeRequirements() {
  return useChallengeWrite(
    (v: { id: number; body: Parameters<typeof adminApi.setChallengeRequirements>[1] }) =>
      adminApi.setChallengeRequirements(v.id, v.body),
  );
}

export function useAttachTag() {
  return useChallengeWrite((v: { challengeId: number; value: string }) =>
    adminApi.attachTag(v.challengeId, v.value),
  );
}

export function useDetachTag() {
  return useChallengeWrite((v: { challengeId: number; value: string }) =>
    adminApi.detachTag(v.challengeId, v.value),
  );
}

// An empty value is not an annotation with nothing in it: it is the annotation removed, which is
// a different request. The caller decides which; this pair just names both.
export function useSetAnnotation() {
  return useChallengeWrite((v: { challengeId: number; key: string; value: string }) =>
    adminApi.setAnnotation(v.challengeId, v.key, v.value),
  );
}

export function useDeleteAnnotation() {
  return useChallengeWrite((v: { challengeId: number; key: string }) =>
    adminApi.deleteAnnotation(v.challengeId, v.key),
  );
}

export function useReorderChallenges() {
  return useChallengeWrite((items: ReadonlyArray<{ id: number; position: number }>) =>
    adminApi.reorderChallenges(items),
  );
}

// A challenge with solves is 409: solves are scoreboard history and are never deleted.
export function useDeleteChallenge() {
  return useChallengeWrite((id: number) => adminApi.deleteChallenge(id));
}

export function useAddFlag() {
  return useChallengeWrite((v: { challengeId: number; body: Parameters<typeof adminApi.addFlag>[1] }) =>
    adminApi.addFlag(v.challengeId, v.body),
  );
}

export function useUpdateFlag() {
  return useChallengeWrite(
    (v: { challengeId: number; flagId: number; body: Parameters<typeof adminApi.updateFlag>[2] }) =>
      adminApi.updateFlag(v.challengeId, v.flagId, v.body),
  );
}

export function useDeleteFlag() {
  return useChallengeWrite((v: { challengeId: number; flagId: number }) =>
    adminApi.deleteFlag(v.challengeId, v.flagId),
  );
}

export function useAddHint() {
  return useChallengeWrite((v: { challengeId: number; body: Parameters<typeof adminApi.addHint>[1] }) =>
    adminApi.addHint(v.challengeId, v.body),
  );
}

export function useUpdateHint() {
  return useChallengeWrite(
    (v: { challengeId: number; hintId: number; body: Parameters<typeof adminApi.updateHint>[2] }) =>
      adminApi.updateHint(v.challengeId, v.hintId, v.body),
  );
}

export function useDeleteHint() {
  return useChallengeWrite((v: { challengeId: number; hintId: number }) =>
    adminApi.deleteHint(v.challengeId, v.hintId),
  );
}

export function useUploadFile() {
  return useChallengeWrite((v: { challengeId: number; file: File }) =>
    adminApi.uploadFile(v.challengeId, v.file),
  );
}

export function useDeleteFile() {
  return useChallengeWrite((fileId: number) => adminApi.deleteFile(fileId));
}
