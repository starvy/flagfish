import type { Reason } from "./reasons";

export type Tone = "info" | "warning" | "danger";

export interface Treatment {
  title: string;
  message: string;
  tone: Tone;
  /** The condition clears on its own — a screen may keep polling instead of dead-ending. */
  transient: boolean;
  /** Somewhere the caller can go to fix this, when the server sends no Location of its own. */
  action: { label: string; to: string } | null;
}

/**
 * The one place a denial becomes something a person can read.
 *
 * Keyed by the server's whole vocabulary, so a reason the server can emit but the UI has not
 * thought about is a type error rather than a blank screen. Screens do not re-derive any of
 * this — they render what PolicyGate hands them.
 */
export const TREATMENTS: Record<Reason, Treatment> = {
  "setup-incomplete": {
    title: "Setup incomplete",
    message: "This instance has not finished its first-run setup.",
    tone: "info",
    transient: false,
    action: { label: "Run setup", to: "/setup" },
  },
  banned: {
    title: "You are banned",
    message: "You have been banned from this CTF. Contact an organiser if you think this is wrong.",
    tone: "danger",
    transient: false,
    action: null,
  },
  "team-banned": {
    title: "Your team is banned",
    message:
      "Your team has been banned from this CTF. Contact an organiser if you think this is wrong.",
    tone: "danger",
    transient: false,
    action: null,
  },
  "password-change-required": {
    title: "Password change required",
    message: "You must set a new password before you can continue.",
    tone: "warning",
    transient: false,
    action: { label: "Change password", to: "/change-password" },
  },
  "not-found": {
    title: "Not found",
    message: "That does not exist, or you are not allowed to know that it does.",
    tone: "info",
    transient: false,
    action: null,
  },
  "auth-required": {
    title: "Sign in to continue",
    message: "This page is for players who are signed in.",
    tone: "info",
    transient: false,
    action: { label: "Sign in", to: "/login" },
  },
  "authentication-required": {
    title: "Sign in to play",
    message: "Submitting a flag requires an account.",
    tone: "info",
    transient: false,
    action: { label: "Sign in", to: "/login" },
  },
  "admins-only": {
    title: "Admins only",
    message: "This challenge is visible to administrators only.",
    tone: "warning",
    transient: false,
    action: null,
  },
  "admin-required": {
    title: "Admins only",
    message: "This is part of the admin surface.",
    tone: "warning",
    transient: false,
    action: null,
  },
  "scores-hidden": {
    title: "The scoreboard is hidden",
    message: "The organisers have hidden the standings.",
    tone: "info",
    transient: false,
    action: null,
  },
  unverified: {
    title: "Verify your email",
    message: "Confirm your email address to unlock the rest of the CTF.",
    tone: "warning",
    transient: false,
    action: { label: "Resend the link", to: "/confirm" },
  },
  "incomplete-profile": {
    title: "Finish your profile",
    message: "A few details are missing from your profile.",
    tone: "warning",
    transient: false,
    action: { label: "Edit profile", to: "/settings" },
  },
  "incomplete-team-profile": {
    title: "Finish your team's profile",
    message: "Your team's profile is missing details a captain needs to fill in.",
    tone: "warning",
    transient: false,
    action: { label: "Edit team", to: "/team" },
  },
  "team-required": {
    title: "Join a team to play",
    message: "This CTF is played in teams. Create one or join an existing one.",
    tone: "info",
    transient: false,
    action: { label: "Find a team", to: "/team" },
  },
  "already-on-team": {
    title: "You are already on a team",
    message: "Leave your current team before joining or creating another.",
    tone: "info",
    transient: false,
    action: { label: "Your team", to: "/team" },
  },
  "team-creation-disabled": {
    title: "Team creation is closed",
    message: "The organisers have turned off new teams. You can still join an existing one.",
    tone: "info",
    transient: false,
    action: null,
  },
  "ctf-not-started": {
    title: "The CTF has not started",
    message: "Come back when the clock starts.",
    tone: "info",
    transient: true,
    action: null,
  },
  "ctf-ended": {
    title: "The CTF is over",
    message: "Submissions are closed. Thanks for playing.",
    tone: "info",
    transient: false,
    action: null,
  },
  paused: {
    title: "The CTF is paused",
    message: "The organisers have paused play. Flags are not being accepted right now.",
    tone: "warning",
    transient: true,
    action: null,
  },
  "already-authed": {
    title: "You are already signed in",
    message: "There is nothing to register.",
    tone: "info",
    transient: false,
    action: { label: "Play", to: "/challenges" },
  },

  // The raw middleware gates. These are transport-level and can land on any screen.
  csrf: {
    title: "Your session moved on",
    message: "This tab's security token is stale. Reload the page and try again.",
    tone: "warning",
    transient: false,
    action: null,
  },
  "rate-limited": {
    title: "Slow down",
    message: "You are sending requests faster than the server will take them.",
    tone: "warning",
    transient: true,
    action: null,
  },
  unavailable: {
    title: "Temporarily unavailable",
    message: "The server could not answer just now. Try again in a moment.",
    tone: "warning",
    transient: true,
    action: null,
  },
  "invalid-credentials": {
    title: "Signed out",
    message: "Your credential is invalid or has expired.",
    tone: "warning",
    transient: false,
    action: { label: "Sign in", to: "/login" },
  },
  "body-too-large": {
    title: "That is too big",
    message: "The request body exceeds the server's limit.",
    tone: "warning",
    transient: false,
    action: null,
  },
  "internal-error": {
    title: "Something broke",
    message: "The server hit an error handling that. It has been logged.",
    tone: "danger",
    transient: false,
    action: null,
  },
  "streaming-unsupported": {
    title: "Live updates unavailable",
    message:
      "This connection cannot carry a live stream; notifications will not update on their own.",
    tone: "warning",
    transient: false,
    action: null,
  },
};
