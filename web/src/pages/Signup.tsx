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
  const [resent, setResent] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      await api.signup(email, password);
      setDone(true);
    } catch (e) {
      setErr((e as ApiError).message || "Could not create the account");
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <AuthShell title="Check your email" foot={<Link to="/login">Back to sign in</Link>}>
        <div className="stack">
          <div className="notice notice-attn">
            A verification link is on its way to <b>{email}</b>. Open it to activate the account, then
            sign in.
          </div>
          <p className="dim" style={{ fontSize: 13.5 }}>
            {resent ? (
              "Sent again."
            ) : (
              <>
                Nothing in your inbox?{" "}
                <a
                  href="#"
                  onClick={(ev) => {
                    ev.preventDefault();
                    void api.resend(email);
                    setResent(true);
                  }}
                >
                  Send it again
                </a>
                .
              </>
            )}
          </p>
        </div>
      </AuthShell>
    );
  }

  return (
    <AuthShell
      title="Create account"
      subtitle="An account holds your apps. Each app gets an API key and its own boards."
      foot={<>Already have an account? <Link to="/login">Sign in</Link></>}
    >
      <div className="stack">
        {err && <div className="notice notice-alarm">{err}</div>}
        <form onSubmit={submit}>
          <Field label="Email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@studio.com" />
          <Field label="Password" type="password" autoComplete="new-password" required minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="at least 8 characters" />
          <button className="btn btn-wide" disabled={busy} type="submit" style={{ marginTop: 6 }}>
            {busy ? "Creating…" : "Create account"}
          </button>
        </form>
      </div>
    </AuthShell>
  );
}
