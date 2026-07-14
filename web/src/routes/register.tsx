import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate, useRouter } from "@tanstack/react-router";
import { api, ApiError } from "../api/client";

export const Route = createFileRoute("/register")({
  component: RegisterPage,
});

function RegisterPage() {
  const navigate = useNavigate();
  const router = useRouter();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const register = useMutation({
    mutationFn: () => api.register({ name, email, password }),
    onSuccess: async () => {
      await router.invalidate();
      await navigate({ to: "/challenges" });
    },
  });

  return (
    <div className="panel auth-panel">
      <h2>
        register<span className="cursor">_</span>
      </h2>
      <form
        className="stack"
        onSubmit={(e) => {
          e.preventDefault();
          register.mutate();
        }}
      >
        <label className="field">
          name
          <input
            autoComplete="username"
            required
            maxLength={128}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
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
            autoComplete="new-password"
            required
            minLength={8}
            maxLength={128}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {register.error && (
          <p className="error">
            {register.error instanceof ApiError ? register.error.message : "registration failed"}
          </p>
        )}
        <button type="submit" disabled={register.isPending}>
          {register.isPending ? "…" : "create account"}
        </button>
        <p className="muted">
          have an account? <Link to="/login">log in</Link>
        </p>
      </form>
    </div>
  );
}
