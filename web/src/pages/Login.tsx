import { type FormEvent, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { useAuth } from "../auth";
import { AuthShell, Field } from "../components";

export default function Login() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState("");
  const [unverified, setUnverified] = useState(false);
  const [busy, setBusy] = useState(false);
  const [params] = useSearchParams();
  const nav = useNavigate();
  const { refresh } = useAuth();

  const verified = params.get("verified");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErr("");
    setUnverified(false);
    setBusy(true);
    try {
      await api.login(email, password);
      await refresh();
      nav("/dashboard");
    } catch (e) {
      const ae = e as ApiError;
      if (ae.status === 403) setUnverified(true);
      else setErr(ae.message || "Login failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell
      title="Sign in"
      subtitle="Access your leaderboard control plane."
      foot={<>No account? <Link to="/signup">Create one</Link></>}
    >
      {verified === "1" && <div className="notice ok">Email verified — you can sign in now.</div>}
      {verified === "0" && <div className="notice err">That verification link is invalid or expired.</div>}
      {err && <div className="notice err">{err}</div>}
      {unverified && (
        <div className="notice err">
          Email not verified. Check your inbox, or{" "}
          <a href="#" onClick={(ev) => { ev.preventDefault(); void api.resend(email); setUnverified(false); setErr("Verification email re-sent."); }}>
            resend it
          </a>.
        </div>
      )}
      <form onSubmit={submit}>
        <Field label="Email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@studio.com" />
        <Field label="Password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} placeholder="••••••••" />
        <div className="spread" style={{ marginTop: 6 }}>
          <Link to="/forgot" className="dim" style={{ fontSize: 13 }}>Forgot password?</Link>
          <button className="btn" disabled={busy} type="submit">{busy ? "…" : "Sign in"}</button>
        </div>
      </form>
    </AuthShell>
  );
}
