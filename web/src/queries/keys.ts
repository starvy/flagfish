import type { NotificationsParams, ScoreboardParams } from "../api/client";
import type { AuditParams, IPOverlapParams, PageParams } from "../api/admin";

/**
 * Query keys mirror the URL they read, so a mutation can invalidate everything under the
 * path it wrote by naming that path's prefix — `["challenges"]` covers `["challenges", 7]`.
 */
export const qk = {
  instance: () => ["instance"] as const,
  me: () => ["me"] as const,

  challenges: () => ["challenges"] as const,
  challenge: (id: number) => ["challenges", id] as const,
  challengeSolves: (id: number) => ["challenges", id, "solves"] as const,

  scoreboard: (params?: ScoreboardParams) =>
    (params === undefined ? ["scoreboard"] : ["scoreboard", params]) as
      | readonly ["scoreboard"]
      | readonly ["scoreboard", ScoreboardParams],
  brackets: () => ["brackets"] as const,

  notifications: (params?: NotificationsParams) =>
    (params === undefined ? ["notifications"] : ["notifications", params]) as
      | readonly ["notifications"]
      | readonly ["notifications", NotificationsParams],

  teams: () => ["teams"] as const,
  team: (id: number) => ["teams", id] as const,
  myTeam: () => ["me", "team"] as const,

  tokens: () => ["tokens"] as const,

  admin: {
    all: () => ["admin"] as const,
    config: () => ["admin", "config"] as const,
    users: (params?: PageParams) =>
      (params === undefined ? ["admin", "users"] : ["admin", "users", params]) as
        | readonly ["admin", "users"]
        | readonly ["admin", "users", PageParams],
    tags: () => ["admin", "tags"] as const,
    brackets: () => ["admin", "brackets"] as const,
    audit: (params?: AuditParams) =>
      (params === undefined ? ["admin", "audit"] : ["admin", "audit", params]) as
        | readonly ["admin", "audit"]
        | readonly ["admin", "audit", AuditParams],
    anticheat: () => ["admin", "anticheat"] as const,
    flagSharing: (params?: PageParams) =>
      (params === undefined
        ? ["admin", "anticheat", "flag-sharing"]
        : ["admin", "anticheat", "flag-sharing", params]) as
        | readonly ["admin", "anticheat", "flag-sharing"]
        | readonly ["admin", "anticheat", "flag-sharing", PageParams],
    ipOverlap: (params?: IPOverlapParams) =>
      (params === undefined
        ? ["admin", "anticheat", "ip-overlap"]
        : ["admin", "anticheat", "ip-overlap", params]) as
        | readonly ["admin", "anticheat", "ip-overlap"]
        | readonly ["admin", "anticheat", "ip-overlap", IPOverlapParams],
    accountReport: (id: number) => ["admin", "anticheat", "accounts", id] as const,
  },
} as const;

/** An operator is looking at the truth, not a cache. Admin reads are always stale. */
export const ADMIN_STALE_TIME = 0;
