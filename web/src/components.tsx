import { useEffect, type CSSProperties, type InputHTMLAttributes, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "./auth";

export const GITHUB = "https://github.com/kodeni-am/leaderboard";

// The mark is the product in miniature: a measured span with one position
// ticked on it. Keep it in step with public/favicon.svg and scripts/gen-favicon.mjs.
export function Logo({ size = 24 }: { size?: number }) {
  return (
    <Link to="/" className="row" style={{ gap: 9, borderBottom: "none", textDecoration: "none" }}>
      <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden>
        <rect x="2" y="11.3" width="20" height="1.4" fill="var(--steel-2)" />
        <rect x="2" y="8.5" width="1.4" height="7" fill="var(--steel-2)" />
        <rect x="20.6" y="8.5" width="1.4" height="7" fill="var(--steel-2)" />
        <rect x="13" y="5" width="2.6" height="14" fill="var(--lamp)" />
      </svg>
      <span
        style={{
          fontFamily: "var(--font-display)",
          fontVariationSettings: '"wdth" 112',
          fontWeight: 700,
          fontSize: 16,
          letterSpacing: "-0.01em",
          color: "var(--steel)",
        }}
      >
        Open<span style={{ color: "var(--signal)" }}>Leaderboard</span>
      </span>
    </Link>
  );
}

// ---- the signature element -------------------------------------------------
// Where one member stands inside the whole ordered set. An approximate rank
// (the histogram tier) renders as a band rather than a tick, so the mark never
// claims more precision than the answer carries.
export function PositionRule({
  rank,
  total,
  who,
  stamp,
  exact = true,
  onInk,
}: {
  rank?: number | null;
  total?: number | null;
  who?: ReactNode;
  stamp?: ReactNode;
  exact?: boolean;
  onInk?: boolean;
}) {
  const placed = rank != null && total != null && total > 0;
  const pos = placed && total > 1 ? ((rank - 1) / (total - 1)) * 100 : 0;
  const band = 5;

  return (
    <div className={"pos-rule" + (onInk ? " rule-on-ink" : "") + (placed ? "" : " rule-empty")}>
      <div className="rule-top">
        <div className="rule-id">
          <span className="rule-rank">
            <span className="label">Rank</span>
            {placed ? (exact ? "" : "≈") + rank.toLocaleString() : "—"}
          </span>
          <span className="rule-who">
            {who ? <>{who} · </> : null}
            of {total != null ? total.toLocaleString() : "—"}
          </span>
        </div>
        {stamp}
      </div>
      <div className="rule-track">
        <div
          className={"rule-tick" + (exact ? "" : " rule-band")}
          style={{ "--pos": `${pos}%`, "--band": `${band}%` } as CSSProperties}
        />
      </div>
      <div className="rule-ends">
        <span>1</span>
        <span>{total != null ? total.toLocaleString() : ""}</span>
      </div>
    </div>
  );
}

// ---- shared furniture ------------------------------------------------------

export function SiteNav({ here }: { here: "home" | "docs" }) {
  const { user } = useAuth();
  return (
    <nav className="topbar">
      <div className="wrap topbar-inner">
        <Logo />
        <div className="row" style={{ gap: 22, flexWrap: "wrap", justifyContent: "flex-end" }}>
          {here === "docs" ? (
            <Link to="/" className="navlink">Home</Link>
          ) : (
            <Link to="/docs" className="navlink">Docs</Link>
          )}
          <a href={GITHUB} className="navlink">GitHub ↗</a>
          {user ? (
            <Link to="/dashboard" className="btn btn-sm">Dashboard</Link>
          ) : (
            <>
              <Link to="/login" className="navlink">Sign in</Link>
              <Link to="/signup" className="btn btn-sm">Get an API key</Link>
            </>
          )}
        </div>
      </div>
    </nav>
  );
}

export function SiteFooter() {
  return (
    <footer className="footer">
      <div className="wrap topbar-inner">
        <Logo />
        <span className="label">Apache-2.0 · self-host or hosted · no lock-in</span>
        <span className="row" style={{ gap: 20 }}>
          <Link to="/docs" className="navlink">Docs</Link>
          <a href={GITHUB} className="navlink">GitHub ↗</a>
        </span>
      </div>
    </footer>
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
  if (!full) return <span className="lamp-dot live" />;
  return (
    <div style={{ minHeight: "60vh", display: "grid", placeItems: "center" }}>
      <div className="row label" style={{ gap: 10 }}>
        <span className="lamp-dot live" />
        Reading
      </div>
    </div>
  );
}

// Styled confirmation dialog (replaces window.confirm). Click the backdrop or
// Cancel to dismiss; Escape also cancels, from anywhere in the dialog.
export function ConfirmDialog({
  title,
  body,
  confirmLabel = "Confirm",
  danger,
  onConfirm,
  onCancel,
}: {
  title: string;
  body: string;
  confirmLabel?: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onCancel]);

  return (
    <div className="modal-overlay" onClick={onCancel}>
      <div
        className="panel modal-card"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="panel-head">
          <span className="label">{danger ? "Confirm · irreversible" : "Confirm"}</span>
        </div>
        <div className="panel-body">
          <h3 style={{ marginBottom: 8 }}>{title}</h3>
          <p className="dim" style={{ fontSize: 14 }}>{body}</p>
          <div className="row" style={{ justifyContent: "flex-end", gap: 10, marginTop: 22 }}>
            <button className="btn btn-ghost btn-sm" onClick={onCancel}>Cancel</button>
            <button className={"btn btn-sm" + (danger ? " btn-danger" : "")} onClick={onConfirm} autoFocus>
              {confirmLabel}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// Centered instrument card used by the account screens.
export function AuthShell({
  title,
  subtitle,
  children,
  foot,
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
  foot?: ReactNode;
}) {
  return (
    <div style={{ minHeight: "100vh", display: "grid", placeItems: "center", padding: 24 }}>
      <div style={{ width: "100%", maxWidth: 410 }}>
        <div style={{ marginBottom: 20 }}>
          <Logo />
        </div>
        <div className="panel rise">
          <div className="panel-head">
            <span className="label">{title}</span>
          </div>
          <div className="panel-body">
            {subtitle && <p className="dim" style={{ fontSize: 14, marginBottom: 20 }}>{subtitle}</p>}
            {children}
          </div>
        </div>
        {foot && (
          <div className="dim" style={{ marginTop: 16, fontSize: 13.5, textAlign: "center" }}>{foot}</div>
        )}
      </div>
    </div>
  );
}
