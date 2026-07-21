import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { adminApi, type SearchParams } from "../../api/admin";
import { ADMIN_STALE_TIME, qk } from "../keys";

export const adminUsersQuery = (params: SearchParams = {}) =>
  queryOptions({
    queryKey: qk.admin.users(params),
    queryFn: () => adminApi.listUsers(params),
    staleTime: ADMIN_STALE_TIME,
  });

function useUserWrite<TVars, TData>(fn: (vars: TVars) => Promise<TData>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.admin.users() });
      // A ban takes the account off the board.
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
    },
  });
}

// Banning yourself is a 409, as is demoting the last admin.
export function useSetUserBanned() {
  return useUserWrite((v: { id: number; banned: boolean }) =>
    adminApi.setUserBanned(v.id, v.banned),
  );
}

export function useSetUserRole() {
  return useUserWrite((v: { id: number; role: "user" | "admin" }) =>
    adminApi.setUserRole(v.id, v.role),
  );
}

export function useSetUserHidden() {
  return useUserWrite((v: { id: number; hidden: boolean }) =>
    adminApi.setUserHidden(v.id, v.hidden),
  );
}

export function useUpdateUser() {
  return useUserWrite(
    (v: { id: number; body: Parameters<typeof adminApi.updateUser>[1] }) =>
      adminApi.updateUser(v.id, v.body),
  );
}

// Unblocks a player the verification mail never reached. Their next request is gated on a
// principal read fresh from the row, so the flip takes effect without them signing in again.
export function useSetUserVerified() {
  return useUserWrite((v: { id: number; verified: boolean }) =>
    adminApi.setUserVerified(v.id, v.verified),
  );
}

// The whole-field version, for when the mailer — not one address — is what is broken.
export function useVerifyAllUsers() {
  return useUserWrite((_: void) => adminApi.verifyAllUsers());
}

// Kills the user's sessions with the flag; they log back in and are walled until they comply.
export function useForcePasswordChange() {
  return useUserWrite((id: number) => adminApi.forcePasswordChange(id));
}

export function useAssignBracket() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { accountId: number; bracketId: number | null }) =>
      adminApi.assignBracket(v.accountId, v.bracketId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.admin.users() });
      void qc.invalidateQueries({ queryKey: qk.scoreboard() });
    },
  });
}
