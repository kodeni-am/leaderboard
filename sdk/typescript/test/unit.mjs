// Unit test of request/error shapes using a mock fetch — no server needed.
// Run after `npm run build`.
import { LeaderboardClient, MemberTakenError, NicknameTakenError } from "../dist/index.js";

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

console.log("PASS: unit");
