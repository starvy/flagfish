import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate, useRouter } from "@tanstack/react-router";
import { api, ApiError } from "../api/client";

export const Route = createFileRoute("/login")({
  validateSearch: (search: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof search.redirect === "string" ? search.redirect : undefined,
  }),
  component: LoginPage,
});

function LoginPage() {
  const { redirect } = Route.useSearch();
  const navigate = useNavigate();
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const login = useMutation({
    mutationFn: () => api.login({ email, password }),
    onSuccess: async () => {
      await router.invalidate();
      await navigate({ to: redirect ?? "/challenges" });
    },
  });

  return (
    <div className="panel auth-panel">
      <h2>
        login<span className="cursor">_</span>
      </h2>
      <form
        className="stack"
        onSubmit={(e) => {
          e.preventDefault();
          login.mutate();
        }}
      >
        <label className="field">
          email
          <input
            type="email"
            autoComplete="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </label>
        <label className="field">
          password
          <input
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {login.error && (
          <p className="error">
            {login.error instanceof ApiError ? login.error.message : "login failed"}
          </p>
        )}
        <button type="submit" disabled={login.isPending}>
          {login.isPending ? "…" : "log in"}
        </button>
        <p className="muted">
          no account? <Link to="/register">register</Link>
        </p>
      </form>
    </div>
  );
}
