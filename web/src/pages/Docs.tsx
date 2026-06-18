import { Link } from "react-router-dom";
import { Logo } from "../components";
import { useAuth } from "../auth";

const GITHUB = "https://github.com/kodeni-am/leaderboard";

const toc = [
  ["getting-started", "Getting started"],
  ["concepts", "Core concepts"],
  ["auth", "Authentication"],
  ["signing", "Signed submissions"],
  ["rest", "REST API"],
  ["sdks", "SDKs"],
];

function Code({ children }: { children: string }) {
  return <pre className="code">{children}</pre>;
}

export default function Docs() {
  const { user } = useAuth();
  return (
    <div style={{ position: "relative", zIndex: 2 }}>
      <nav className="container spread site-nav" style={{ minHeight: 72 }}>
        <Logo />
        <div className="row nav-links" style={{ gap: 20 }}>
          <Link to="/" className="dim mono" style={{ fontSize: 13, letterSpacing: "0.04em" }}>HOME</Link>
          <a href={GITHUB} className="dim mono" style={{ fontSize: 13, letterSpacing: "0.04em" }}>GITHUB ↗</a>
          {user ? <Link to="/dashboard" className="btn btn-sm">Dashboard</Link> : <Link to="/signup" className="btn btn-sm">Get started</Link>}
        </div>
      </nav>

      <div className="container" style={{ paddingTop: 16, paddingBottom: 40 }}>
        <div className="eyebrow">DOCUMENTATION</div>
        <h1 style={{ fontSize: 40, margin: "10px 0 8px" }}>Developer docs</h1>
        <p className="dim" style={{ fontSize: 17, maxWidth: 640, marginTop: 0 }}>
          Everything to submit scores and read ranks — concepts, the REST API, and the SDKs.
        </p>
      </div>

      <div className="container docs-grid" style={{ paddingBottom: 80 }}>
        <aside className="toc">
          {toc.map(([id, label]) => (
            <a key={id} href={`#${id}`}>{label}</a>
          ))}
        </aside>

        <main>
          <section id="getting-started" className="docs-section">
            <h2>Getting started</h2>
            <p>1. Create an account and verify your email. 2. In the <Link to="/dashboard">dashboard</Link>, create an <b>app</b> — you’ll get an <b>API key</b> (shown once). 3. Define a <b>board</b>. 4. Drop in an SDK:</p>
            <Code>{`# TypeScript / JavaScript
npm install @openleaderboard/sdk

# Go
go get github.com/kodeni-am/leaderboard/pkg/sdk

# Unity (Package Manager → Add from git URL)
https://github.com/kodeni-am/leaderboard.git?path=/sdk/unity`}</Code>
            <p>Then submit a score and read a rank:</p>
            <Code>{`import { LeaderboardClient } from "@openleaderboard/sdk";

const lb = new LeaderboardClient(API_URL, API_KEY);

await lb.submitScore("high_scores", playerId, 1500);
const me  = await lb.getRank("high_scores", playerId);   // { rank, score, exact }
const top = await lb.getTop("high_scores", 10);`}</Code>
          </section>

          <section id="concepts" className="docs-section">
            <h2>Core concepts</h2>
            <ul style={{ lineHeight: 1.8 }}>
              <li><b>Board</b> — a ranking. Configure <span className="muted-code">sort_order</span> (<span className="mono">desc</span> = higher wins, <span className="mono">asc</span> = lower/faster wins), <span className="muted-code">update_policy</span> (<span className="mono">best</span> / <span className="mono">last</span> / <span className="mono">increment</span>), and <span className="muted-code">tie_break</span> (<span className="mono">lexical</span> or <span className="mono">firstToReach</span>).</li>
              <li><b>Windows</b> — a board can keep a permanent <span className="mono">all</span>-time ranking plus rolling <span className="mono">daily</span> / <span className="mono">weekly</span> / <span className="mono">monthly</span> / seasonal windows that reset automatically.</li>
              <li><b>Segments</b> — slice a board by region, platform, or any cohort key you pass at submit time (e.g. <span className="mono">region=eu</span>).</li>
              <li><b>Friends &amp; neighbors</b> — rank a specific set of members against each other, or fetch the players just above and below a given player.</li>
              <li><b>Write-behind</b> — submits are durably logged and ranked asynchronously (typically within milliseconds), so writes stay fast and bursts are absorbed.</li>
              <li><b>Scale</b> — rank reads stay O(log N) at any size. For very large boards you can enable an <b>approximate-rank tier</b>: create the board with <span className="muted-code">approx_rank: true</span> (plus an <span className="muted-code">approx_min</span>/<span className="muted-code">approx_max</span> score range) and read with <span className="muted-code">?approx=true</span> to get an O(buckets) estimate. The response’s <span className="muted-code">exact</span> flag tells you whether a rank is exact or estimated.</li>
            </ul>
          </section>

          <section id="auth" className="docs-section">
            <h2>Authentication</h2>
            <p>There are two planes. <b>Game clients</b> authenticate with an app’s <b>API key</b>:</p>
            <Code>{`Authorization: Bearer lb_your_api_key
# or
X-API-Key: lb_your_api_key`}</Code>
            <p>The <b>dashboard</b> uses your account session (a cookie) plus an <span className="muted-code">X-App-Id</span> header — you don’t need to handle this directly; the dashboard does it for you. Keep API keys server-side or in your game build; never expose your account session.</p>
          </section>

          <section id="signing" className="docs-section">
            <h2>Signed submissions (anti-cheat)</h2>
            <p>Optional. In the <Link to="/dashboard">dashboard</Link>, each app can require score submissions to be <b>HMAC-signed</b>, so a leaked API key alone can’t post forged scores. Enable <b>“Require signed submissions”</b> and copy the app’s <b>signing secret</b> (rotate it anytime). Keep the secret on your game’s <b>server</b> — never in a public client build.</p>
            <p>Sign a canonical message and include <span className="muted-code">sig</span>, <span className="muted-code">ts</span> (unix seconds), and a <span className="muted-code">nonce</span> in the submit body. The SDKs do this for you when you pass the secret:</p>
            <Code>{`// the canonical message is newline-joined:
//   app \\n board \\n member \\n score \\n ts \\n nonce
// sig = hex( HMAC_SHA256(signing_secret, message) )

const lb = new LeaderboardClient(apiUrl, apiKey, {
  appId: "app_…",
  signingSecret: "lbsk_…",   // server-side only
});
await lb.submit("high_scores", playerId, 1500); // signed automatically`}</Code>
            <p className="dim" style={{ fontSize: 13 }}>Submissions are rejected (HTTP 401) if the signature is missing/invalid or the timestamp is outside a few minutes of server time. Apps that don’t enable this keep API-key-only auth.</p>
          </section>

          <section id="rest" className="docs-section">
            <h2>REST API</h2>
            <p>Base path <span className="muted-code">/v1</span>. All board endpoints accept the API key auth above; query endpoints accept <span className="muted-code">?segment=</span> and <span className="muted-code">?window=</span> (a literal id like <span className="mono">d=2026-06-13</span> or a keyword <span className="mono">daily</span>/<span className="mono">weekly</span>/<span className="mono">monthly</span>).</p>
            <table className="lb" style={{ marginTop: 10 }}>
              <thead><tr><th>Method · Path</th><th>Purpose</th></tr></thead>
              <tbody>
                {[
                  ["POST /v1/boards", "Define a board"],
                  ["POST /v1/boards/{board}/scores", "Submit a score (write-behind)"],
                  ["GET /v1/boards/{board}/rank?member=", "A member's rank (add &approx=true for the histogram estimate)"],
                  ["GET /v1/boards/{board}/top?n=", "Top N"],
                  ["GET /v1/boards/{board}/page?offset=&limit=", "Paginate"],
                  ["GET /v1/boards/{board}/neighbors?member=&k=", "Me ± k"],
                  ["POST /v1/boards/{board}/friends", "Rank an explicit member list"],
                ].map(([m, p]) => (
                  <tr key={m}><td className="mono" style={{ color: "var(--text)" }}>{m}</td><td className="dim">{p}</td></tr>
                ))}
              </tbody>
            </table>
            <p style={{ marginTop: 18 }}>Submit example:</p>
            <Code>{`curl -X POST $API_URL/v1/boards/high_scores/scores \\
  -H "Authorization: Bearer $API_KEY" \\
  -d '{"member":"player-1","score":1500,"segments":["all","region=eu"]}'

# → 202 {"accepted": true}`}</Code>
          </section>

          <section id="sdks" className="docs-section">
            <h2>SDKs</h2>
            <p><b>Go</b></p>
            <Code>{`c := sdk.New(apiURL, apiKey)
c.Submit(ctx, "high_scores", sdk.Submission{Member: "p1", Score: 1500})
top, _ := c.Top(ctx, "high_scores", 10, sdk.QueryOpts{})`}</Code>
            <p><b>Unity / C#</b> (async, works on WebGL/IL2CPP)</p>
            <Code>{`var lb = new LeaderboardClient(apiUrl, apiKey);
await lb.SubmitScoreAsync("high_scores", playerId, 1500);
var top = await lb.GetTopAsync("high_scores", 10);`}</Code>
            <p><b>TypeScript</b></p>
            <Code>{`const lb = new LeaderboardClient(apiUrl, apiKey);
await lb.submitScore("high_scores", playerId, 1500);
const near = await lb.getNeighbors("high_scores", playerId, 5);`}</Code>
          </section>
        </main>
      </div>

      <footer style={{ borderTop: "1px solid var(--line)" }}>
        <div className="container spread" style={{ padding: "26px 24px", flexWrap: "wrap", gap: 14 }}>
          <Logo />
          <a href={GITHUB} className="mono" style={{ fontSize: 12, letterSpacing: "0.04em" }}>GITHUB ↗</a>
        </div>
      </footer>
    </div>
  );
}
