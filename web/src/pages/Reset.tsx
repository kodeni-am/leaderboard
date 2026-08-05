import { type FormEvent, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { AuthShell, Field } from "../components";

export default function Reset() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const [password, setPassword] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      await api.reset(token, password);
      setDone(true);
    } catch (e) {
      setErr((e as ApiError).message || "Could not update the password");
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell title="Choose a new password" foot={<Link to="/login">Back to sign in</Link>}>
      <div className="stack">
        {!token && (
          <div className="notice notice-alarm">
            This page needs the reset token from your email. Open the link in that message.
          </div>
        )}
        {done ? (
          <div className="notice notice-attn">Password updated. Sign in with it now.</div>
        ) : (
          <>
            {err && <div className="notice notice-alarm">{err}</div>}
            <form onSubmit={submit}>
              <Field label="New password" type="password" required minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="at least 8 characters" />
              <button className="btn btn-wide" disabled={busy || !token} type="submit" style={{ marginTop: 6 }}>
                {busy ? "Updating…" : "Update password"}
              </button>
            </form>
          </>
        )}
      </div>
    </AuthShell>
  );
}
