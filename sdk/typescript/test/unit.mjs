// Unit test of request/error shapes using a mock fetch — no server needed.
// Run after `npm run build`.
import { LeaderboardClient, MemberTakenError, NicknameTakenError, PlayerTokenError } from "../dist/index.js";

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL:", msg);
    process.exit(1);
  }
}

const calls = [];
let nextResponse = () => new Response(JSON.stringify({ user_id: "plr_x", nickname: "Kai" }), { status: 201 });
const lb = new LeaderboardClient("http://test", "key", {
  fetch: (url, init) => {
    calls.push({ url, init });
    return Promise.resolve(nextResponse());
  },
});

// registerUser with a member claim: the body carries both fields.
nextResponse = () => new Response(JSON.stringify({ user_id: "surfer-1", nickname: "Kai" }), { status: 201 });
const claimed = await lb.registerUser("Kai", { member: "surfer-1" });
assert(claimed.user_id === "surfer-1", "claim echoes member as user_id");
let body = JSON.parse(calls.at(-1).init.body);
assert(body.nickname === "Kai" && body.member === "surfer-1", `claim body ${calls.at(-1).init.body}`);

// registerUser without a member: no member key in the body at all.
nextResponse = () => new Response(JSON.stringify({ user_id: "plr_x", nickname: "Kai2" }), { status: 201 });
await lb.registerUser("Kai2");
body = JSON.parse(calls.at(-1).init.body);
assert(!("member" in body), `mint body must omit member: ${calls.at(-1).init.body}`);

// The two 409 causes map to distinct error types.
nextResponse = () => new Response(JSON.stringify({ error: "member_taken" }), { status: 409 });
let memberTaken = false;
try {
  await lb.registerUser("Kai", { member: "surfer-1" });
} catch (e) {
  memberTaken = e instanceof MemberTakenError;
}
assert(memberTaken, "member_taken throws MemberTakenError");

nextResponse = () => new Response(JSON.stringify({ error: "nickname_taken" }), { status: 409 });
let nickTaken = false;
try {
  await lb.registerUser("Kai");
} catch (e) {
  nickTaken = e instanceof NicknameTakenError && !(e instanceof MemberTakenError);
}
assert(nickTaken, "nickname_taken throws NicknameTakenError");

// player token: sent as a header on every request, read through the getter at call time
let current = "t".repeat(64);
const tokCalls = [];
const tl = new LeaderboardClient("http://test", "key", {
  playerToken: () => current,
  fetch: (url, init) => {
    tokCalls.push(init);
    return Promise.resolve(new Response(JSON.stringify({ accepted: true }), { status: 202 }));
  },
});
await tl.submitScore("hs", "surfer-1", 5);
assert(tokCalls.at(-1).headers["X-Player-Token"] === "t".repeat(64), "token header sent");
current = "u".repeat(64);
await tl.deleteUser("surfer-1");
assert(tokCalls.at(-1).headers["X-Player-Token"] === "u".repeat(64), "getter read per call");
current = undefined;
await tl.getTop("hs", 5);
assert(!("X-Player-Token" in tokCalls.at(-1).headers), "no header without a token");

// token errors map to PlayerTokenError with the server's code
for (const [status, code] of [[403, "player_token_mismatch"], [401, "player_token_required"], [400, "invalid_player_token"]]) {
  const el = new LeaderboardClient("http://test", "key", {
    fetch: () => Promise.resolve(new Response(JSON.stringify({ error: code }), { status })),
  });
  let got = null;
  try {
    await el.submitScore("hs", "m", 1);
  } catch (e) {
    got = e;
  }
  assert(got instanceof PlayerTokenError && got.code === code && got.status === status, `${code} -> PlayerTokenError`);
}

console.log("PASS: unit");
