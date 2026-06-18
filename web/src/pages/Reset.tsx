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
      setErr((e as ApiError).message || "Reset failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell title="Choose a new password" foot={<Link to="/login">Back to sign in</Link>}>
      {!token && <div className="notice err">Missing reset token. Use the link from your email.</div>}
      {done ? (
        <div className="notice ok">Password updated. You can sign in now.</div>
      ) : (
        <>
          {err && <div className="notice err">{err}</div>}
          <form onSubmit={submit}>
            <Field label="New password" type="password" required minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="at least 8 characters" />
            <button className="btn" disabled={busy || !token} type="submit" style={{ width: "100%", justifyContent: "center", marginTop: 6 }}>
              {busy ? "…" : "Update password"}
            </button>
          </form>
        </>
      )}
    </AuthShell>
  );
}
