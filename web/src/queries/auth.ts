import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Me, type Session } from "../api/client";
import { resetNotificationStream } from "../lib/notifications";
import { qk } from "./keys";

export const meQuery = queryOptions({
  queryKey: qk.me(),
  queryFn: () => api.me(),
  staleTime: 60_000,
  retry: false,
});

export type { Me, Session };

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.login,
    onSuccess: (session) => {
      qc.setQueryData(qk.me(), undefined);
      // The whole cache belongs to the previous identity; nothing in it survives a new one.
      void qc.invalidateQueries();
      return session;
    },
  });
}

export function useRegister() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.register,
    onSuccess: () => {
      void qc.invalidateQueries();
    },
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.logout,
    onSuccess: () => {
      resetNotificationStream();
      qc.clear();
    },
  });
}

export function useUpdateMe() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.updateMe,
    onSuccess: (me) => {
      qc.setQueryData(qk.me(), me);
    },
  });
}

export const registrationFieldsQuery = queryOptions({
  queryKey: qk.registrationFields(),
  queryFn: () => api.registrationFields(),
  staleTime: 60_000,
  retry: false,
});

// Answering a field can clear the profile-complete gate, so a success refreshes /me — the value
// the whole gated surface reads from.
export function useAnswerFields() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.answerFields,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.me() });
    },
  });
}

export function useChangeName() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.changeName,
    onSuccess: (me) => {
      qc.setQueryData(qk.me(), me);
    },
  });
}

// The response carries the pending address so /me immediately shows "check your inbox". The live
// email does not move until the confirmation token is used, so nothing else in the cache changes yet.
export function useChangeEmail() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.changeEmail,
    onSuccess: (me) => {
      qc.setQueryData(qk.me(), me);
    },
  });
}

// The confirmation lands on whatever device opened the new inbox; when it is this one, the live email
// has just changed, so refresh /me.
export function useConfirmEmailChange() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.confirmEmailChange,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.me() });
    },
  });
}

export function useChangePassword() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.changePassword,
    // A forced change clears the flag that was gating every other page. The change also deletes
    // the account's API tokens server-side — a write this mutation does not make itself, so the
    // "invalidate what you wrote" rule does not reach it and the settings list would otherwise
    // keep rendering tokens that no longer exist.
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.me() });
      void qc.invalidateQueries({ queryKey: qk.tokens() });
    },
  });
}

export function useResetRequest() {
  return useMutation({ mutationFn: api.resetRequest });
}

export function useResetApply() {
  return useMutation({ mutationFn: api.resetApply });
}

export function useVerifyResend() {
  return useMutation({ mutationFn: api.verifyResend });
}

export function useVerifyConfirm() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.verifyConfirm,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.me() });
    },
  });
}
