import { type FormEvent, useState } from "react";
import { Link } from "react-router-dom";
import { api, ApiError } from "../api";
import { AuthShell, Field } from "../components";

export default function Signup() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      await api.signup(email, password);
      setDone(true);
    } catch (e) {
      setErr((e as ApiError).message || "Signup failed");
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <AuthShell title="Check your email" foot={<Link to="/login">Back to sign in</Link>}>
        <div className="notice ok">
          We sent a verification link to <b>{email}</b>. Click it to activate your account, then sign in.
        </div>
        <p className="dim" style={{ fontSize: 13, marginBottom: 0 }}>
          Didn’t get it?{" "}
          <a href="#" onClick={(ev) => { ev.preventDefault(); void api.resend(email); }}>Resend</a>.
        </p>
      </AuthShell>
    );
  }

  return (
    <AuthShell
      title="Create account"
      subtitle="Spin up leaderboards in minutes."
      foot={<>Already have an account? <Link to="/login">Sign in</Link></>}
    >
      {err && <div className="notice err">{err}</div>}
      <form onSubmit={submit}>
        <Field label="Email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@studio.com" />
        <Field label="Password" type="password" autoComplete="new-password" required minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="at least 8 characters" />
        <button className="btn" disabled={busy} type="submit" style={{ width: "100%", justifyContent: "center", marginTop: 6 }}>
          {busy ? "…" : "Create account"}
        </button>
      </form>
    </AuthShell>
  );
}
