import { type FormEvent, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { useAuth } from "../auth";
import { AuthShell, Field } from "../components";

export default function Login() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState("");
  const [info, setInfo] = useState("");
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
      else setErr(ae.message || "Sign-in failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell
      title="Sign in"
      subtitle="Your apps, keys, and boards."
      foot={<>No account? <Link to="/signup">Create one</Link></>}
    >
      <div className="stack">
        {verified === "1" && <div className="notice notice-attn">Email verified. Sign in to continue.</div>}
        {verified === "0" && <div className="notice notice-alarm">That verification link is invalid or expired. Sign in to send a new one.</div>}
        {info && <div className="notice notice-attn">{info}</div>}
        {err && <div className="notice notice-alarm">{err}</div>}
        {unverified && (
          <div className="notice notice-alarm">
            This email is not verified yet. Check your inbox, or{" "}
            <a
              href="#"
              onClick={(ev) => {
                ev.preventDefault();
                void api.resend(email);
                setUnverified(false);
                setErr("");
                setInfo("Verification email sent again — check your inbox.");
              }}
            >
              send it again
            </a>
            .
          </div>
        )}
        <form onSubmit={submit}>
          <Field label="Email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@studio.com" />
          <Field label="Password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} placeholder="••••••••" />
          <div className="spread" style={{ marginTop: 6 }}>
            <Link to="/forgot" className="dim" style={{ fontSize: 13.5, borderBottom: "none" }}>Forgot password?</Link>
            <button className="btn" disabled={busy} type="submit">{busy ? "Signing in…" : "Sign in"}</button>
          </div>
        </form>
      </div>
    </AuthShell>
  );
}
