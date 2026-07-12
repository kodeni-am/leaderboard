// End-to-end test of the TS SDK against a running leaderboardd.
// Requires the server up (e.g. `docker compose up -d leaderboardd`) and an
// app API key in LB_API_KEY. Run after `npm run build`.
import { LeaderboardClient, NotFoundError, NicknameTakenError, MemberTakenError } from "../dist/index.js";

const BASE = process.env.BASE ?? "http://localhost:8080";
// App creation requires a logged-in account; create an app in the dashboard
// (or via the signup/login API) and pass its API key here.
const KEY = process.env.LB_API_KEY;

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL:", msg);
    process.exit(1);
  }
}

if (!KEY) {
  console.log("SKIP: set LB_API_KEY=<an app's API key> to run the e2e test.");
  process.exit(0);
}

const lb = new LeaderboardClient(BASE, KEY);

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

// Users & nicknames.
const nick = `Tester-${Date.now()}`;
const u = await lb.registerUser(nick);
assert(u.user_id.startsWith("plr_") && u.nickname === nick, `registerUser ${JSON.stringify(u)}`);

let conflict = false;
try {
  await lb.registerUser(nick.toUpperCase());
} catch (e) {
  conflict = e instanceof NicknameTakenError;
}
assert(conflict, "duplicate nickname throws NicknameTakenError");

const byNick = await lb.getUserByNickname(nick.toLowerCase());
assert(byNick.user_id === u.user_id, "getUserByNickname resolves case-insensitively");

const renamed = await lb.renameUser(u.user_id, `${nick}-2`);
assert(renamed.nickname === `${nick}-2`, "renameUser");

await lb.submitScore("high", u.user_id, 999);
await new Promise((res) => setTimeout(res, 1500));
const enriched = await lb.getTop("high", 5);
const mine = enriched.find((e) => e.member === u.user_id);
assert(mine && mine.nickname === `${nick}-2`, `top carries nickname ${JSON.stringify(enriched)}`);

// Claim an existing anonymous member id in place: the nickname attaches to
// the row already on the board — no resubmit, no delete.
const anonId = `surfer-${Date.now()}`;
await lb.submitScore("high", anonId, 777);
await new Promise((res) => setTimeout(res, 1500));
const claimNick = `Claimed-${Date.now()}`;
const claimed = await lb.registerUser(claimNick, { member: anonId });
assert(claimed.user_id === anonId, `claim echoes member: ${JSON.stringify(claimed)}`);
const afterClaim = await lb.getRank("high", anonId);
assert(afterClaim.score === 777 && afterClaim.nickname === claimNick, `claimed row keeps score + gains nickname ${JSON.stringify(afterClaim)}`);
let memberConflict = false;
try {
  await lb.registerUser(`Other-${Date.now()}`, { member: anonId });
} catch (e) {
  memberConflict = e instanceof MemberTakenError;
}
assert(memberConflict, "re-claiming a member throws MemberTakenError");

// Moderation: remove one entry, then delete a player entirely.
await lb.submitScore("high", "mallory", 9999);
await new Promise((res) => setTimeout(res, 1500));
await lb.removeScore("high", "mallory");
let removed = false;
try {
  await lb.getRank("high", "mallory");
} catch (e) {
  removed = e instanceof NotFoundError;
}
assert(removed, "mallory removed from board");
await lb.removeScore("high", "mallory"); // idempotent

const victim = await lb.registerUser("ToDelete-" + Date.now());
await lb.submitScore("high", victim.user_id, 123);
await new Promise((res) => setTimeout(res, 1500));
await lb.deleteUser(victim.user_id);
let unregistered = false;
try {
  await lb.getUser(victim.user_id);
} catch (e) {
  unregistered = e instanceof NotFoundError;
}
assert(unregistered, "deleted player unregistered");

console.log("TS SDK e2e: PASS ✅");
