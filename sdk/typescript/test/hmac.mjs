// Cross-validates the SDK's HMAC against the known Go/openssl signature for
// fixed inputs (integer score). Run after `npm run build`.
import { signSubmission } from "../dist/index.js";

const got = await signSubmission("topsecret", "app1", "high", "alice", 1500, 1700000000, "nonce-1");
const want = "16e5c9f69e99dbd0629183b0a794cb2e116aff1a602d1faa2de54a89afab71b6";

if (got === want) {
  console.log("HMAC cross-check: MATCH (TS == Go == openssl) ✅");
} else {
  console.error(`HMAC MISMATCH\n  got:  ${got}\n  want: ${want}`);
  process.exit(1);
}
