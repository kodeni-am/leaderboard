import { type ReactNode } from "react";
import { Link } from "react-router-dom";
import { PositionRule, SiteFooter, SiteNav } from "../components";

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

function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section id={id} className="doc-section">
      <h2 style={{ fontSize: 27 }}>{title}</h2>
      {children}
    </section>
  );
}

export default function Docs() {
  return (
    <>
      <SiteNav here="docs" />

      <div className="wrap" style={{ paddingTop: 44, paddingBottom: 34 }}>
        <span className="label">Documentation</span>
        <h1 style={{ fontSize: "clamp(34px, 4.4vw, 52px)", margin: "12px 0 12px" }}>
          Submit a score. Read a rank.
        </h1>
        <p className="lead" style={{ maxWidth: 620 }}>
          The concepts, the REST API, and the SDKs — everything between an API key and a working
          leaderboard.
        </p>
      </div>

      <div className="wrap docs-grid" style={{ paddingBottom: 70 }}>
        <aside className="toc">
          {toc.map(([id, label]) => (
            <a key={id} href={`#${id}`}>{label}</a>
          ))}
        </aside>

        <main>
          <Section id="getting-started" title="Getting started">
            <p>
              Create an account and verify your email. In the <Link to="/dashboard">dashboard</Link>,
              create an <b>app</b> — you get an <b>API key</b>, shown once. Define a <b>board</b>.
              Then install an SDK:
            </p>
            <Code>{`# TypeScript / JavaScript
npm install @openleaderboard/sdk

# Go
go get github.com/kodeni-am/leaderboard/pkg/sdk

# Unity (Package Manager → Add from git URL)
https://github.com/kodeni-am/leaderboard.git?path=/sdk/unity`}</Code>
            <p>Submit a score and read a rank:</p>
            <Code>{`import { LeaderboardClient } from "@openleaderboard/sdk";

const lb = new LeaderboardClient(API_URL, API_KEY);

await lb.submitScore("high_scores", playerId, 1500);
const me  = await lb.getRank("high_scores", playerId);   // { rank, score, exact }
const top = await lb.getTop("high_scores", 10);`}</Code>
          </Section>

          <Section id="concepts" title="Core concepts">
            <ul>
              <li>
                <b>Board</b> — a ranking. Configure <span className="tok">sort_order</span>{" "}
                (<span className="mono">desc</span> = higher wins, <span className="mono">asc</span> =
                lower or faster wins), <span className="tok">update_policy</span>{" "}
                (<span className="mono">best</span> / <span className="mono">last</span> /{" "}
                <span className="mono">increment</span>), and <span className="tok">tie_break</span>{" "}
                (<span className="mono">lexical</span> or <span className="mono">firstToReach</span>).
              </li>
              <li>
                <b>Windows</b> — a board keeps a permanent <span className="mono">all</span>-time
                ranking plus rolling <span className="mono">daily</span> /{" "}
                <span className="mono">weekly</span> / <span className="mono">monthly</span> /
                seasonal windows. A score lands in the bucket for its <b>event time</b> (UTC), which
                defaults to when the server receives it — pass <span className="tok">time</span> on
                submit (the session start, say) so a run that crosses midnight counts for the day it
                began.
              </li>
              <li>
                <b>Segments</b> — slice a board by region, platform, or any cohort key you pass at
                submit time, e.g. <span className="mono">region=eu</span>.
              </li>
              <li>
                <b>Friends and neighbours</b> — rank an explicit set of members against each other, or
                fetch the players just above and below one player.
              </li>
              <li>
                <b>Write-behind</b> — submits are durably logged and ranked asynchronously, typically
                within milliseconds, so writes stay fast and bursts are absorbed.
              </li>
              <li>
                <b>Scale</b> — rank reads stay O(log N) at any size. Very large boards can also enable
                an <b>approximate-rank tier</b>: create the board with{" "}
                <span className="tok">approx_rank: true</span> plus an{" "}
                <span className="tok">approx_min</span>/<span className="tok">approx_max</span> score
                range, and read with <span className="tok">?approx=true</span> for an O(buckets)
                estimate.
              </li>
            </ul>

            <div className="panel" style={{ marginTop: 20 }}>
              <div className="panel-head">
                <span className="label">Exact vs approximate</span>
                <span className="tok">exact: false</span>
              </div>
              <div className="panel-body">
                <PositionRule
                  rank={61_044_219}
                  total={104_882_391}
                  who={<span className="mono">ze1da</span>}
                  exact={false}
                  stamp={<span className="num" style={{ fontSize: 12, color: "var(--steel)" }}>bucket estimate</span>}
                />
                <p className="dim" style={{ fontSize: 13.5, marginTop: 16 }}>
                  Every rank response carries an <span className="tok">exact</span> flag. Exact ranks
                  are a point on the board; an approximate rank is a band, and the width is the
                  bucket. Top-N and “me ± neighbours” are always exact.
                </p>
              </div>
            </div>

            <div className="notice notice-attn" style={{ marginTop: 20 }}>
              <b>Time windows reset at UTC midnight.</b> There is no per-board timezone yet. For a
              non-UTC daily reset, either slice by region with <span className="tok">segments</span>,
              or drive a <span className="mono">custom</span> window whose id you rotate at your local
              midnight. Do not shift the submitted <span className="tok">time</span> to fake a local
              day: it lands the score in the right bucket but corrupts{" "}
              <span className="mono">firstToReach</span> tie-breaks — the timestamp also encodes
              achievement order — and mislabels the bucket id.
            </div>
          </Section>

          <Section id="auth" title="Authentication">
            <p>There are two planes. <b>Game clients</b> authenticate with an app's <b>API key</b>:</p>
            <Code>{`Authorization: Bearer lb_your_api_key
# or
X-API-Key: lb_your_api_key`}</Code>
            <p>
              The <b>dashboard</b> uses your account session (a cookie) plus an{" "}
              <span className="tok">X-App-Id</span> header — you never handle this directly. Keep API
              keys server-side or inside your game build; never expose your account session.
            </p>
          </Section>

          <Section id="signing" title="Signed submissions">
            <p>
              Optional anti-cheat. Each app can require score submissions to be <b>HMAC-signed</b>, so
              a leaked API key alone cannot post forged scores. Turn on{" "}
              <b>Require signed submissions</b> in the <Link to="/dashboard">dashboard</Link> and copy
              the app's <b>signing secret</b> — rotate it whenever you need to. Keep the secret on
              your game's <b>server</b>, never in a public client build.
            </p>
            <p>
              Sign a canonical message and include <span className="tok">sig</span>,{" "}
              <span className="tok">ts</span> (unix seconds), and a <span className="tok">nonce</span>{" "}
              in the submit body. The SDKs do this for you when you pass the secret:
            </p>
            <Code>{`// the canonical message is newline-joined:
//   app \\n board \\n member \\n score \\n ts \\n nonce
// sig = hex( HMAC_SHA256(signing_secret, message) )

const lb = new LeaderboardClient(apiUrl, apiKey, {
  appId: "app_…",
  signingSecret: "lbsk_…",   // server-side only
});
await lb.submit("high_scores", playerId, 1500); // signed automatically`}</Code>
            <p className="dim" style={{ fontSize: 13.5 }}>
              A submission is rejected with 401 if the signature is missing or invalid, or if the
              timestamp is more than a few minutes from server time. Apps that leave signing off keep
              API-key-only auth.
            </p>
          </Section>

          <Section id="rest" title="REST API">
            <p>
              Base path <span className="tok">/v1</span>. Board endpoints take the API key auth above;
              query endpoints accept <span className="tok">?segment=</span> and{" "}
              <span className="tok">?window=</span> — a literal id like{" "}
              <span className="mono">d=2026-06-13</span> or a keyword{" "}
              <span className="mono">daily</span> / <span className="mono">weekly</span> /{" "}
              <span className="mono">monthly</span>.
            </p>
            <div className="panel">
              <div className="panel-head">
                <span className="label">Endpoint</span>
                <span className="label">Purpose</span>
              </div>
              <div className="tablewrap">
              <table className="lb">
                <tbody>
                  {[
                    ["POST /v1/boards", "Define a board"],
                    ["POST /v1/boards/{board}/scores", "Submit a score (write-behind)"],
                    ["GET /v1/boards/{board}/rank?member=", "One member's rank — add &approx=true for the estimate"],
                    ["GET /v1/boards/{board}/top?n=", "Top N"],
                    ["GET /v1/boards/{board}/page?offset=&limit=", "Paginate"],
                    ["GET /v1/boards/{board}/neighbors?member=&k=", "Me ± k"],
                    ["POST /v1/boards/{board}/friends", "Rank an explicit member list"],
                  ].map(([m, p]) => (
                    <tr key={m}>
                      <td className="mono" style={{ fontSize: 13 }}>{m}</td>
                      <td className="dim">{p}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              </div>
            </div>
            <p>Submit example:</p>
            <Code>{`curl -X POST $API_URL/v1/boards/high_scores/scores \\
  -H "Authorization: Bearer $API_KEY" \\
  -d '{"member":"player-1","score":1500,"segments":["all","region=eu"]}'

# → 202 {"accepted": true}`}</Code>
          </Section>

          <Section id="sdks" title="SDKs">
            <p><b>Go</b></p>
            <Code>{`c := sdk.New(apiURL, apiKey)
c.Submit(ctx, "high_scores", sdk.Submission{Member: "p1", Score: 1500})
top, _ := c.Top(ctx, "high_scores", 10, sdk.QueryOpts{})`}</Code>
            <p><b>Unity / C#</b> — async, works on WebGL and IL2CPP</p>
            <Code>{`var lb = new LeaderboardClient(apiUrl, apiKey);
await lb.SubmitScoreAsync("high_scores", playerId, 1500);
var top = await lb.GetTopAsync("high_scores", 10);`}</Code>
            <p><b>TypeScript</b></p>
            <Code>{`const lb = new LeaderboardClient(apiUrl, apiKey);
await lb.submitScore("high_scores", playerId, 1500);
const near = await lb.getNeighbors("high_scores", playerId, 5);`}</Code>
          </Section>
        </main>
      </div>

      <SiteFooter />
    </>
  );
}
