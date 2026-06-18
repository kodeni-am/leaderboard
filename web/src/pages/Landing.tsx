import { Link } from "react-router-dom";
import { Logo } from "../components";
import { useAuth } from "../auth";

const GITHUB = "https://github.com/kodeni-am/leaderboard";

const board = [
  { r: 1, m: "shadowfax", s: "1,284,920" },
  { r: 2, m: "nullptr", s: "1,190,044" },
  { r: 3, m: "ze1da", s: "1,002,318" },
  { r: 4, m: "kp_winters", s: "984,771" },
  { r: 5, m: "0xdeadbeef", s: "921,560" },
];

const stats = [
  { v: "92k", l: "rank reads / sec" },
  { v: "~222µs", l: "p99 read latency" },
  { v: "100M+", l: "entries / board" },
  { v: "O(log N)", l: "rank, any size" },
];

const features = [
  {
    k: "01",
    t: "Flat reads at any size",
    d: "Rank is intrinsic to the sorted set — ZRANK is O(log N). A top-10 query costs the same at 10k or 100M entries. We benchmarked it: p99 barely moves.",
  },
  {
    k: "02",
    t: "Durable log, rebuildable cache",
    d: "Writes hit a durable, partitioned log first, then fan out to the in-memory ranking tier. Bursts are absorbed; the cache is always rebuildable from the log.",
  },
  {
    k: "03",
    t: "Scales sideways, stays ordered",
    d: "Partitioned by player, consumed by Redis Streams consumer groups. Add workers to add throughput — per-player order is preserved for last-write-wins.",
  },
];

const boardTypes = ["Global all-time", "Daily / weekly / seasonal", "Region / platform segments", "Friends & me ± neighbors"];

export default function Landing() {
  const { user } = useAuth();
  return (
    <div style={{ position: "relative", zIndex: 2 }}>
      {/* nav */}
      <nav className="container spread" style={{ height: 72 }}>
        <Logo />
        <div className="row" style={{ gap: 20 }}>
          <a href={GITHUB} className="dim mono" style={{ fontSize: 13, letterSpacing: "0.04em" }}>GITHUB ↗</a>
          {user ? (
            <Link to="/dashboard" className="btn btn-sm">Dashboard</Link>
          ) : (
            <>
              <Link to="/login" className="dim mono" style={{ fontSize: 13, letterSpacing: "0.04em" }}>SIGN IN</Link>
              <Link to="/signup" className="btn btn-sm">Get started</Link>
            </>
          )}
        </div>
      </nav>

      {/* hero */}
      <section className="container" style={{ display: "grid", gridTemplateColumns: "1.1fr 0.9fr", gap: 56, alignItems: "center", padding: "60px 24px 72px" }}>
        <div className="reveal">
          <div className="tag" style={{ marginBottom: 22 }}>OPEN SOURCE · APACHE-2.0</div>
          <h1 style={{ fontSize: "clamp(38px, 5.2vw, 66px)", lineHeight: 0.98 }}>
            Leaderboards that stay <span className="accent">instant</span> at a hundred million players.
          </h1>
          <p className="dim" style={{ fontSize: 18, maxWidth: 520, marginTop: 22 }}>
            A fast, trustworthy, open-source leaderboard API for game developers. Submit a score, read a rank — in microseconds, at any scale, from any engine.
          </p>
          <div className="row" style={{ gap: 14, marginTop: 30 }}>
            <Link to="/signup" className="btn">Start building</Link>
            <a href={GITHUB} className="btn btn-ghost">View on GitHub</a>
          </div>
          <div className="row mono" style={{ gap: 18, marginTop: 26, fontSize: 12, color: "var(--text-faint)", letterSpacing: "0.04em" }}>
            <span>● Go SDK</span>
            <span>● Unity / C#</span>
            <span>● TypeScript</span>
          </div>
        </div>

        {/* HUD leaderboard card */}
        <div className="panel reveal" style={{ animationDelay: "0.12s", padding: 0, overflow: "hidden", boxShadow: "0 30px 80px -40px rgba(0,0,0,0.8)" }}>
          <div className="spread" style={{ padding: "14px 18px", borderBottom: "1px solid var(--line)" }}>
            <span className="mono" style={{ fontSize: 12, letterSpacing: "0.1em", color: "var(--text-dim)" }}>GLOBAL · ALL-TIME</span>
            <span className="row mono" style={{ gap: 7, fontSize: 11, color: "var(--accent)", letterSpacing: "0.12em" }}>
              <span style={{ width: 7, height: 7, borderRadius: 999, background: "var(--accent)", animation: "pulse 1.4s infinite ease-in-out", display: "inline-block" }} />
              LIVE
            </span>
          </div>
          <table className="lb">
            <tbody>
              {board.map((b) => (
                <tr key={b.r}>
                  <td className="rank">{String(b.r).padStart(2, "0")}</td>
                  <td className="mono" style={{ color: "var(--text)" }}>{b.m}</td>
                  <td className="score">{b.s}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="spread mono" style={{ padding: "12px 18px", borderTop: "1px solid var(--line)", fontSize: 11, color: "var(--text-faint)" }}>
            <span>rank lookup</span>
            <span className="accent">0.21 ms · exact</span>
          </div>
        </div>
      </section>

      {/* stat strip */}
      <section className="container">
        <div className="panel" style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", padding: 0 }}>
          {stats.map((s, i) => (
            <div key={s.l} style={{ padding: "26px 22px", borderLeft: i ? "1px solid var(--line)" : "none" }}>
              <div className="mono accent" style={{ fontSize: 30, fontWeight: 700 }}>{s.v}</div>
              <div className="eyebrow" style={{ marginTop: 6 }}>{s.l}</div>
            </div>
          ))}
        </div>
        <div className="dim mono" style={{ fontSize: 11, marginTop: 10, textAlign: "right", color: "var(--text-faint)" }}>
          * indicative single-node benchmark — see repo for methodology
        </div>
      </section>

      {/* features */}
      <section className="container" style={{ padding: "80px 24px 20px" }}>
        <div className="eyebrow">WHY IT STAYS FAST</div>
        <h2 style={{ fontSize: 34, marginTop: 12, marginBottom: 36, maxWidth: 640 }}>
          The hard part is staying instant <span className="dim">as the board grows.</span>
        </h2>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: 16 }}>
          {features.map((f) => (
            <div key={f.k} className="panel" style={{ borderTop: "2px solid var(--accent)" }}>
              <div className="mono accent" style={{ fontSize: 13, letterSpacing: "0.1em" }}>{f.k}</div>
              <h3 style={{ fontSize: 19, margin: "12px 0 10px" }}>{f.t}</h3>
              <p className="dim" style={{ margin: 0, fontSize: 14.5 }}>{f.d}</p>
            </div>
          ))}
        </div>
      </section>

      {/* board types */}
      <section className="container" style={{ padding: "44px 24px" }}>
        <div className="row" style={{ flexWrap: "wrap", gap: 10 }}>
          <span className="eyebrow" style={{ marginRight: 8 }}>EVERY BOARD SHAPE:</span>
          {boardTypes.map((b) => (
            <span key={b} className="tag" style={{ color: "var(--text)", background: "var(--bg-elev2)", borderColor: "var(--line-bright)" }}>{b}</span>
          ))}
        </div>
      </section>

      {/* quickstart */}
      <section className="container" style={{ padding: "40px 24px 80px" }}>
        <div style={{ display: "grid", gridTemplateColumns: "0.8fr 1.2fr", gap: 40, alignItems: "center" }}>
          <div>
            <div className="eyebrow">QUICKSTART</div>
            <h2 style={{ fontSize: 30, margin: "12px 0 14px" }}>Ten lines to your first rank.</h2>
            <p className="dim" style={{ fontSize: 15 }}>
              Create an app in the dashboard for an API key, then drop in an SDK. Scores are write-behind — durably logged and ranked within milliseconds.
            </p>
            <Link to="/signup" className="btn" style={{ marginTop: 8 }}>Get your API key</Link>
          </div>
          <pre className="panel mono" style={{ fontSize: 13.5, lineHeight: 1.7, overflowX: "auto", color: "var(--text-dim)", margin: 0 }}>
{`import { LeaderboardClient } from "@openleaderboard/sdk";

const lb = new LeaderboardClient(API_URL, API_KEY);

`}<span className="accent">await lb.submitScore("high", playerId, 1500);</span>{`

const me  = await lb.getRank("high", playerId);
const top = await lb.getTop("high", 10);
`}</pre>
        </div>
      </section>

      {/* footer */}
      <footer style={{ borderTop: "1px solid var(--line)", marginTop: 20 }}>
        <div className="container spread" style={{ padding: "26px 24px", flexWrap: "wrap", gap: 14 }}>
          <Logo />
          <span className="dim mono" style={{ fontSize: 12 }}>Apache-2.0 · self-host or hosted · no lock-in</span>
          <a href={GITHUB} className="mono" style={{ fontSize: 12, letterSpacing: "0.04em" }}>GITHUB ↗</a>
        </div>
      </footer>
    </div>
  );
}
