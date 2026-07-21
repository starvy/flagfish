import { queryOptions } from "@tanstack/react-query";
import { api } from "../api/client";
import { qk } from "./keys";

// A user's public profile. A hidden or banned account answers 404 to a non-admin — a denial the page
// renders, not an error boundary to trip — so callers that want to distinguish it read e.status.
export const userProfileQuery = (id: number) =>
  queryOptions({
    queryKey: qk.user(id),
    queryFn: () => api.userProfile(id),
    staleTime: 60_000,
    retry: false,
  });
