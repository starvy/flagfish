import { queryOptions } from "@tanstack/react-query";
import { api } from "../api/client";
import { qk } from "./keys";

// Branding and the clock. It drives theme resolution and every "is the CTF open" decision,
// so it must not spam the server — but it must also not be trusted forever.
export const instanceQuery = queryOptions({
  queryKey: qk.instance(),
  queryFn: () => api.instance(),
  staleTime: 5 * 60_000,
  retry: false,
});
