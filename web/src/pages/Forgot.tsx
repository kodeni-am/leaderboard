import { type FormEvent, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { AuthShell, Field } from "../components";

export default function Forgot() {
  const [email, setEmail] = useState("");
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api.forgot(email);
    } finally {
      setBusy(false);
      setDone(true);
    }
  }

  return (
    <AuthShell title="Reset password" foot={<Link to="/login">Back to sign in</Link>}>
      {done ? (
        <div className="notice ok">If an account exists for <b>{email}</b>, a reset link is on its way.</div>
      ) : (
        <form onSubmit={submit}>
          <Field label="Email" type="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@studio.com" />
          <button className="btn" disabled={busy} type="submit" style={{ width: "100%", justifyContent: "center", marginTop: 6 }}>
            {busy ? "…" : "Send reset link"}
          </button>
        </form>
      )}
    </AuthShell>
  );
}
