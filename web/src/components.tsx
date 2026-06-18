import { type InputHTMLAttributes, type ReactNode } from "react";
import { Link } from "react-router-dom";

export function Logo({ size = 22 }: { size?: number }) {
  return (
    <Link to="/" className="row" style={{ textDecoration: "none", color: "var(--text)" }}>
      <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden>
        <rect x="2" y="13" width="5" height="9" fill="var(--text-faint)" />
        <rect x="9.5" y="6" width="5" height="16" fill="var(--accent)" />
        <rect x="17" y="10" width="5" height="12" fill="var(--cyan)" />
      </svg>
      <span style={{ fontFamily: "var(--font-display)", fontWeight: 700, letterSpacing: "-0.01em", fontSize: 16 }}>
        Open<span style={{ color: "var(--accent)" }}>Leaderboard</span>
      </span>
    </Link>
  );
}

export function Field({ label, ...props }: { label: string } & InputHTMLAttributes<HTMLInputElement>) {
  return (
    <label className="field">
      <span>{label}</span>
      <input {...props} />
    </label>
  );
}

export function Spinner({ full }: { full?: boolean }) {
  const dot = (
    <span
      style={{
        display: "inline-block",
        width: 9,
        height: 9,
        background: "var(--accent)",
        animation: "pulse 1s infinite ease-in-out",
      }}
    />
  );
  if (!full) return dot;
  return (
    <div style={{ minHeight: "60vh", display: "grid", placeItems: "center" }}>
      <div className="mono dim" style={{ letterSpacing: "0.2em", textTransform: "uppercase", fontSize: 12 }}>
        loading…
      </div>
    </div>
  );
}

// Centered card layout used by the auth screens.
export function AuthShell({ title, subtitle, children, foot }: { title: string; subtitle?: string; children: ReactNode; foot?: ReactNode }) {
  return (
    <div style={{ minHeight: "100vh", display: "grid", placeItems: "center", padding: 24, position: "relative", zIndex: 2 }}>
      <div style={{ width: "100%", maxWidth: 400 }}>
        <div style={{ marginBottom: 24 }}>
          <Logo />
        </div>
        <div className="panel" style={{ padding: 28 }}>
          <div className="eyebrow" style={{ marginBottom: 8 }}>{title}</div>
          {subtitle && <p className="dim" style={{ marginTop: 0, marginBottom: 22, fontSize: 14 }}>{subtitle}</p>}
          {children}
        </div>
        {foot && <div className="dim" style={{ marginTop: 18, fontSize: 13, textAlign: "center" }}>{foot}</div>}
      </div>
    </div>
  );
}
