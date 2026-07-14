import { useState } from "react";
import { useMutation, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { api, ApiError } from "../../api/client";
import type { CreatedToken } from "../../api/client";
import { meQuery, tokensQuery } from "../../queries";
import { ThemeSwitcher } from "../../theme/ThemeSwitcher";

export const Route = createFileRoute("/_auth/profile")({
  loader: ({ context }) =>
    Promise.all([
      context.queryClient.ensureQueryData(meQuery),
      context.queryClient.ensureQueryData(tokensQuery),
    ]),
  component: ProfilePage,
});

function ProfilePage() {
  const { data: me } = useSuspenseQuery(meQuery);

  return (
    <div className="stack-lg">
      <div className="panel">
        <h2>account</h2>
        <p>
          <strong>{me.name}</strong> · {me.email}
          {me.is_admin && <span className="tag"> admin</span>}
        </p>
        <p className="muted">
          role: {me.role} · email {me.verified ? "verified" : "not verified"}
        </p>
      </div>
      <div className="panel">
        <h2>appearance</h2>
        <ThemeSwitcher />
      </div>
      <ChangePassword />
      <Tokens />
    </div>
  );
}

function ChangePassword() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");

  const change = useMutation({
    mutationFn: () => api.changePassword({ current_password: current, new_password: next }),
    onSuccess: () => {
      setCurrent("");
      setNext("");
    },
  });

  return (
    <div className="panel">
      <h2>change password</h2>
      <form
        className="stack"
        onSubmit={(e) => {
          e.preventDefault();
          change.mutate();
        }}
      >
        <label className="field">
          current password
          <input
            type="password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
          />
        </label>
        <label className="field">
          new password
          <input
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            maxLength={128}
            value={next}
            onChange={(e) => setNext(e.target.value)}
          />
        </label>
        {change.error && (
          <p className="error">
            {change.error instanceof ApiError ? change.error.message : "could not change password"}
          </p>
        )}
        {change.isSuccess && <p className="ok">password changed</p>}
        <button type="submit" disabled={change.isPending}>
          {change.isPending ? "…" : "change password"}
        </button>
      </form>
    </div>
  );
}

function Tokens() {
  const queryClient = useQueryClient();
  const { data } = useSuspenseQuery(tokensQuery);
  const [description, setDescription] = useState("");
  const [ttlHours, setTtlHours] = useState("720");
  const [minted, setMinted] = useState<CreatedToken | null>(null);
  const [copied, setCopied] = useState(false);

  const create = useMutation({
    mutationFn: () =>
      api.createToken({
        description: description || undefined,
        ttl_hours: Number(ttlHours) || undefined,
      }),
    onSuccess: async (token) => {
      setMinted(token);
      setCopied(false);
      setDescription("");
      await queryClient.invalidateQueries({ queryKey: tokensQuery.queryKey });
    },
  });

  const revoke = useMutation({
    mutationFn: (id: number) => api.deleteToken(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: tokensQuery.queryKey }),
  });

  const copy = async () => {
    if (!minted) return;
    await navigator.clipboard.writeText(minted.token);
    setCopied(true);
  };

  const tokens = data.tokens ?? [];

  return (
    <div className="panel">
      <h2>API tokens</h2>
      <form
        className="flag-form"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <input
          placeholder="description (optional)"
          maxLength={255}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <label className="field" style={{ flexDirection: "row", alignItems: "center", gap: "0.4rem" }}>
          ttl&nbsp;h
          <input
            type="number"
            min={1}
            style={{ width: "5.5rem" }}
            value={ttlHours}
            onChange={(e) => setTtlHours(e.target.value)}
          />
        </label>
        <button type="submit" disabled={create.isPending}>
          {create.isPending ? "…" : "mint token"}
        </button>
      </form>
      {create.error && (
        <p className="error">
          {create.error instanceof ApiError ? create.error.message : "could not create token"}
        </p>
      )}
      {minted && (
        <div className="token-reveal">
          <code>{minted.token}</code>
          <button className="ghost" onClick={() => void copy()}>
            {copied ? "copied ✓" : "copy"}
          </button>
        </div>
      )}
      {minted && (
        <p className="muted">shown once — it cannot be retrieved again, store it now.</p>
      )}

      {tokens.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>description</th>
              <th>created</th>
              <th>expires</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {tokens.map((t) => (
              <tr key={t.id}>
                <td>{t.description ?? <span className="muted">—</span>}</td>
                <td className="muted">
                  {t.created_at ? new Date(t.created_at).toLocaleString() : "—"}
                </td>
                <td className="muted">{new Date(t.expires_at).toLocaleString()}</td>
                <td>
                  <button
                    className="danger"
                    onClick={() => revoke.mutate(t.id)}
                    disabled={revoke.isPending}
                  >
                    revoke
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {revoke.error && (
        <p className="error">
          {revoke.error instanceof ApiError ? revoke.error.message : "could not revoke token"}
        </p>
      )}
    </div>
  );
}
