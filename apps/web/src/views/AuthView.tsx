import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { CurrentUser } from "../api/types";
import { Button, InlineAlert } from "../components/ui";
import { Icon } from "../components/Icon";

export function LoginView({ onAuthenticated }: { onAuthenticated: (user: CurrentUser) => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string>();
  const [submitting, setSubmitting] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError(undefined);
    try {
      onAuthenticated(await api.login({ username, password }));
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="auth-screen">
      <form className="auth-card" onSubmit={(event) => void submit(event)}>
        <div className="auth-brand"><span><Icon name="box" size={28} /></span><div><strong>AgentBox</strong><small>Operations Console</small></div></div>
        <div className="auth-heading"><p className="eyebrow">Account access</p><h1>Sign in</h1><p>Use an administrator or user account to continue.</p></div>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        <label className="field"><span>Username</span><input autoComplete="username" autoFocus value={username} onChange={(event) => setUsername(event.target.value)} required /></label>
        <label className="field"><span>Password</span><input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required /></label>
        <Button type="submit" variant="primary" busy={submitting}>Sign in</Button>
        <p className="auth-hint">First-run administrator: <code>admin</code> / <code>admin123</code>. The password must be changed after the first sign-in.</p>
      </form>
    </div>
  );
}

export function PasswordChangeView({ user, onChanged, onLogout }: { user: CurrentUser; onChanged: (user: CurrentUser) => void; onLogout: () => void }) {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<string>();
  const [submitting, setSubmitting] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (newPassword !== confirmPassword) {
      setError("New passwords do not match.");
      return;
    }
    setSubmitting(true);
    setError(undefined);
    try {
      onChanged(await api.changePassword({ currentPassword, newPassword }));
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="auth-screen">
      <form className="auth-card" onSubmit={(event) => void submit(event)}>
        <div className="auth-brand"><span><Icon name="box" size={28} /></span><div><strong>AgentBox</strong><small>{user.login}</small></div></div>
        <div className="auth-heading"><p className="eyebrow">Security required</p><h1>Change temporary password</h1><p>Choose a private password with at least eight characters before using the console.</p></div>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        <label className="field"><span>Current password</span><input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} required /></label>
        <label className="field"><span>New password</span><input type="password" autoComplete="new-password" minLength={8} value={newPassword} onChange={(event) => setNewPassword(event.target.value)} required /></label>
        <label className="field"><span>Confirm new password</span><input type="password" autoComplete="new-password" minLength={8} value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} required /></label>
        <div className="auth-actions"><Button type="button" variant="ghost" onClick={onLogout}>Sign out</Button><Button type="submit" variant="primary" busy={submitting}>Change password</Button></div>
      </form>
    </div>
  );
}
