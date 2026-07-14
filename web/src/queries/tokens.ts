import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { qk } from "./keys";

export const tokensQuery = queryOptions({
  queryKey: qk.tokens(),
  queryFn: () => api.tokens(),
  staleTime: 30_000,
});

// The plaintext token comes back exactly once, on create — the caller must show it there and
// then, because no later read can produce it again.
export function useCreateToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createToken,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tokens() });
    },
  });
}

export function useDeleteToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api.deleteToken(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tokens() });
    },
  });
}
