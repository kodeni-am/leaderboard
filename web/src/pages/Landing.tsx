import { Fragment, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { GITHUB, PositionRule, SiteFooter, SiteNav } from "../components";

// Illustrative reads on a demo board — the panel is labelled DEMO BOARD so the
// numbers are never mistaken for a live instance. One read is deliberately
// approximate: deep in the tail, the histogram tier answers with a band.
const READS = [
  { who: "shadowfax", rank: 1, ms: "0.19", exact: true },
  { who: "kp_winters", rank: 27, ms: "0.21", exact: true },
  { who: "nullptr", rank: 4_208_113, ms: "0.22", exact: true },
  { who: "ze1da", rank: 61_044_219, ms: "0.24", exact: false },
  { who: "0xdeadbeef", rank: 99_213_480, ms: "0.20", exact: true },
];
const DEMO_TOTAL = 104_882_391;

const TOP5 = [
  { r: 1, m: "shadowfax", s: "1,284,920" },
  { r: 2, m: "nullptr", s: "1,190,044" },
  { r: 3, m: "ze1da", s: "1,002,318" },
  { r: 4, m: "kp_winters", s: "984,771" },
  { r: 5, m: "0xdeadbeef", s: "921,560" },
];

// Measured numbers, straight from the repo's load-test table (README →
// Benchmarking). The axis is fixed at 0–300µs so the bars are read against a
// zero baseline: the story is that they barely differ.
const LATENCY = [
  { size: "1,000", reads: "92,883", p99: 242 },
  { size: "10,000", reads: "92,451", p99: 239 },
  { size: "100,000", reads: "91,830", p99: 239 },
  { size: "1,000,000", reads: "92,997", p99: 222 },
];
const AXIS_MAX = 300;

const MECHANISMS = [
  {
    part: "ZADD GT · ZRANK",
    title: "Rank is intrinsic, not computed",
    body: "Position lives in the sorted set, so reading it is O(log N) — there is no scan to grow and no counter to keep in step. Best-score-wins is a single atomic ZADD GT, never a read-modify-write.",
  },
  {
    part: "Redis Streams",
    title: "The log is the source of truth",
    body: "Submissions land in a durable, partitioned log before they are ranked. Bursts are absorbed, writes stay fast, and the ranking tier is a cache you can rebuild from the log at any time.",
  },
  {
    part: "Consumer groups",
    title: "Add workers, keep the order",
    body: "The log is partitioned by player and consumed by a group, so throughput scales sideways while per-player ordering survives — last-write-wins still means the last write.",
  },
];

const SHAPES = [
  ["Global, all-time", "Where does this player stand overall?"],
  ["Daily · weekly · monthly · seasonal", "Who won today, and who won last season?"],
  ["Region · platform · cohort segments", "Who is top of Europe on console?"],
  ["Friends and me ± neighbours", "Who is just ahead of me right now?"],
];

function usePrefersReducedMotion() {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia("(prefers-reduced-motion: reduce)");
    setReduced(mq.matches);
    const on = () => setReduced(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return reduced;
}

export default function Landing() {
  const reduced = usePrefersReducedMotion();
  const [i, setI] = useState(0);

  // The hero reads cycle on their own; with reduced motion it holds one read.
  useEffect(() => {
    if (reduced) return;
    const t = setInterval(() => setI((n) => (n + 1) % READS.length), 2600);
    return () => clearInterval(t);
  }, [reduced]);

  const read = READS[i];

  return (
    <>
      <SiteNav here="home" />

      {/* hero — the live read is the thesis, stated before the headline */}
      <section className="band">
        <div className="wrap">
          <div className="row" style={{ gap: 14, marginBottom: 18, flexWrap: "wrap" }}>
            <span className="label">Open source · Apache-2.0</span>
            <span className="label" style={{ color: "var(--etch-2)" }}>/</span>
            <span className="label">Leaderboard API for games</span>
          </div>

          <div className="panel rise">
            <div className="panel-head">
              <span className="label">Demo board · global · all-time</span>
              <span className="row label" style={{ gap: 7 }}>
                <span className="lamp-dot live" />
                Live read
              </span>
            </div>
            <div className="panel-body" style={{ padding: "22px 20px 18px" }}>
              <PositionRule
                rank={read.rank}
                total={DEMO_TOTAL}
                who={<span className="mono">{read.who}</span>}
                exact={read.exact}
                stamp={
                  <span className="num" style={{ fontSize: 12, color: "var(--steel)" }}>
                    {read.ms} ms · {read.exact ? "exact" : "approximate"}
                  </span>
                }
              />
            </div>
          </div>

          <div className="hero-grid" style={{ marginTop: 44 }}>
            <div>
              <h1>Everyone knows exactly where they stand.</h1>
              <p className="lead" style={{ maxWidth: 520, marginTop: 22 }}>
                An open-source leaderboard API for game developers. Submit a score, read a rank — and
                pay the same read cost whether the board holds ten thousand players or a hundred
                million.
              </p>
              <div className="row" style={{ gap: 12, marginTop: 26, flexWrap: "wrap" }}>
                <Link to="/signup" className="btn">Get an API key</Link>
                <a href={GITHUB} className="btn btn-ghost" style={{ borderBottom: "1px solid var(--etch-2)" }}>
                  View on GitHub
                </a>
              </div>
              <div className="row" style={{ gap: 18, marginTop: 24, flexWrap: "wrap" }}>
                <span className="label">SDKs</span>
                <span className="label" style={{ color: "var(--steel)" }}>Go · Unity/C# · TypeScript</span>
              </div>
            </div>

            <div className="panel">
              <div className="panel-head">
                <span className="label">Top 5 · demo board</span>
                <span className="label">Score</span>
              </div>
              <div className="tablewrap">
                <table className="lb">
                  <tbody>
                    {TOP5.map((b) => (
                      <tr key={b.r}>
                        <td className="rank">{String(b.r).padStart(2, "0")}</td>
                        <td className="mono">{b.m}</td>
                        <td className="score">{b.s}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* the measured claim */}
      <section className="band-tight">
        <div className="wrap split">
          <div>
            <span className="label">Measured</span>
            <h2 style={{ margin: "12px 0 14px" }}>The read does not get slower.</h2>
            <p className="dim">
              That is the whole bet. Rank comes out of the sorted set itself, so a top-10 query or a
              single player's position costs the same at a thousand members as at a million — and the
              architecture is built for boards into the 100M+ range.
            </p>
          </div>

          <div className="panel">
            <div className="panel-head">
              <span className="label">GetRank p99 by board size</span>
              <span className="caption">0 – {AXIS_MAX} µs</span>
            </div>
            <div className="panel-body">
              <div className="bars">
                {LATENCY.map((r) => (
                  <Fragment key={r.size}>
                    <div className="bars-size">{r.size}</div>
                    <div className="bars-track">
                      <div className="bars-fill" style={{ width: `${(r.p99 / AXIS_MAX) * 100}%` }} />
                    </div>
                    <div className="bars-val">{r.p99} µs</div>
                  </Fragment>
                ))}
              </div>
            </div>
            <div className="panel-foot">
              <p className="caption">
                ~92,000 reads/s sustained at every size. Indicative local run — one Docker Redis on a
                laptop, not a production benchmark. Harness: cmd/loadtest, engine mode.
              </p>
            </div>
          </div>
        </div>
      </section>

      {/* how it holds up */}
      <section className="band">
        <div className="wrap">
          <span className="label">Mechanism</span>
          <h2 style={{ margin: "12px 0 28px", maxWidth: 660 }}>
            Three parts do the work.
          </h2>
          <div className="mech-grid">
            {MECHANISMS.map((m) => (
              <div key={m.part} className="panel">
                <div className="panel-head">
                  <span className="label">{m.part}</span>
                </div>
                <div className="panel-body">
                  <h3 style={{ marginBottom: 10 }}>{m.title}</h3>
                  <p className="dim" style={{ fontSize: 14.5 }}>{m.body}</p>
                </div>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* board shapes */}
      <section className="band-tight">
        <div className="wrap">
          <div className="panel">
            <div className="panel-head">
              <span className="label">Board shapes</span>
              <span className="label">What it answers</span>
            </div>
            <div className="tablewrap">
            <table className="lb">
              <tbody>
                {SHAPES.map(([shape, answers]) => (
                  <tr key={shape}>
                    <td style={{ width: "42%" }}>{shape}</td>
                    <td className="dim">{answers}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
          </div>
        </div>
      </section>

      {/* quickstart */}
      <section className="band">
        <div className="wrap split">
          <div>
            <span className="label">Quickstart</span>
            <h2 style={{ margin: "12px 0 14px" }}>Ten lines to your first rank.</h2>
            <p className="dim">
              Create an app in the dashboard to get an API key, then drop in an SDK. Scores are
              write-behind: durably logged on submit and ranked within milliseconds.
            </p>
            <Link to="/signup" className="btn" style={{ marginTop: 22 }}>Get an API key</Link>
          </div>
          <pre className="code">
{`import { LeaderboardClient } from "@openleaderboard/sdk";

const lb = new LeaderboardClient(API_URL, API_KEY);

`}<b>await lb.submitScore("high_scores", playerId, 1500);</b>{`

const me  = await lb.getRank("high_scores", playerId);
const top = await lb.getTop("high_scores", 10);`}
          </pre>
        </div>
      </section>

      <SiteFooter />
    </>
  );
}
