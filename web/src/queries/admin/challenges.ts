import { useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi } from "../../api/admin";
import { qk } from "../keys";

// Every admin write to a challenge also changes what players see, so both boards are dropped.
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
