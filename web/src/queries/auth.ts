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

export function useChangePassword() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.changePassword,
    // A forced change clears the flag that was gating every other page.
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.me() });
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
