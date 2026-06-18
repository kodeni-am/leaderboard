// End-to-end test of the TS SDK against a running leaderboardd.
// Requires the server up (e.g. `docker compose up -d leaderboardd`) with
// ADMIN_TOKEN. Run after `npm run build`.
import { LeaderboardClient, NotFoundError } from "../dist/index.js";

const BASE = process.env.BASE ?? "http://localhost:8080";
const ADMIN = process.env.ADMIN_TOKEN ?? "dev-admin-token";

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL:", msg);
    process.exit(1);
  }
}

// Provision an app (admin op, not part of the client SDK).
const appResp = await fetch(`${BASE}/v1/apps`, {
  method: "POST",
  headers: { "X-Admin-Token": ADMIN, "Content-Type": "application/json" },
  body: JSON.stringify({ name: "ts-sdk-e2e" }),
});
assert(appResp.status === 201, `create app -> ${appResp.status}`);
const { api_key } = await appResp.json();

const lb = new LeaderboardClient(BASE, api_key);

await lb.createBoard("high", { sortOrder: "desc", updatePolicy: "best" });

const r = await lb.submitScore("high", "alice", 300);
assert(r.accepted === true, "submit accepted");
await lb.submitScore("high", "bob", 500);
await lb.submitScore("high", "carol", 100);

// Write-behind: wait for the consumer to apply the log.
await new Promise((res) => setTimeout(res, 1500));

const bob = await lb.getRank("high", "bob");
assert(bob.rank === 1 && bob.score === 500, `bob rank=${bob.rank} score=${bob.score}`);

const top = await lb.getTop("high", 2);
assert(top.length === 2 && top[0].member === "bob" && top[1].member === "alice", `top ${JSON.stringify(top)}`);

const near = await lb.getNeighbors("high", "alice", 1);
assert(near.length === 3 && near[1].member === "alice", `neighbors ${JSON.stringify(near)}`);

const fr = await lb.getFriends("high", ["carol", "bob"]);
assert(fr.length === 2 && fr[0].member === "bob" && fr[0].rank === 1, `friends ${JSON.stringify(fr)}`);

let notFound = false;
try {
  await lb.getRank("high", "ghost");
} catch (e) {
  notFound = e instanceof NotFoundError;
}
assert(notFound, "missing member throws NotFoundError");

console.log("TS SDK e2e: PASS ✅");
