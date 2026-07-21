import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type AttemptResult, type UnlockResult } from "../api/client";
import { qk } from "./keys";

export const challengesQuery = queryOptions({
  queryKey: qk.challenges(),
  queryFn: () => api.challenges(),
  staleTime: 15_000,
});

export const challengeQuery = (id: number) =>
  queryOptions({
    queryKey: qk.challenge(id),
    queryFn: () => api.challenge(id),
    staleTime: 15_000,
  });

// One query per loaded page, keyed by the opaque cursor that opens it — the same keyset shape the
// admin submissions feed uses. An absent cursor is the first page.
export const challengeSolvesQuery = (id: number, cursor?: string) =>
  queryOptions({
    queryKey: qk.challengeSolves(id, cursor),
    queryFn: () => api.challengeSolves(id, cursor),
    staleTime: 15_000,
  });

export interface AttemptVars {
  id: number;
  flag: string;
}

/**
 * Submits a flag and renders whatever the server says.
 *
 * There is no optimistic update here and there must never be one: the verdict — correct,
 * incorrect, already solved, first blood, and what the solve was worth — is a fact the server
 * computes under a lock. Guessing it client-side means telling a player they scored when they
 * did not. A wrong flag is a 200; only a locked challenge, an exhausted attempt budget and a
 * missing challenge are errors.
 */
export function useAttempt() {
  const qc = useQueryClient();
  return useMutation<AttemptResult, Error, AttemptVars>({
    mutationFn: ({ id, flag }) => api.attempt(id, flag),
    onSuccess: (result, { id }) => {
      // Even a wrong answer spends an attempt, so the challenge is stale either way.
      void qc.invalidateQueries({ queryKey: qk.challenge(id) });
      if (result.status === "correct") {
        void qc.invalidateQueries({ queryKey: qk.challenges() });
        void qc.invalidateQueries({ queryKey: qk.scoreboard() });
        // The header shows the score, and in teams mode it reads the team, not the board — invalidate
        // it too so the points badge moves without a reload. Users mode reads the board, above.
        void qc.invalidateQueries({ queryKey: qk.myTeam() });
      }
    },
  });
}

export interface UnlockVars {
  challengeId: number;
  hintId: number;
}

/**
 * Unlocks a hint. The failure modes are UI states, not crashes: 402 when the player cannot
 * afford it, 409 when it is already unlocked, 403 when a prerequisite hint comes first.
 */
export function useUnlockHint() {
  const qc = useQueryClient();
  return useMutation<UnlockResult, Error, UnlockVars>({
    mutationFn: ({ challengeId, hintId }) => api.unlockHint(challengeId, hintId),
    onSuccess: (_r, { challengeId }) => {
      void qc.invalidateQueries({ queryKey: qk.challenge(challengeId) });
      // The unlock is charged against the score.
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
    },
  });
}

/** Fetches a challenge file as a blob, so the caller can save it under the server's filename. */
export function useDownloadFile() {
  return useMutation({ mutationFn: (id: number) => api.downloadFile(id) });
}
