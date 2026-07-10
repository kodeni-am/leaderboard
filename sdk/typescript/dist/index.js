/**
 * OpenLeaderboard TypeScript SDK.
 *
 * Dependency-free client over the Fetch API — runs in browsers and Node 18+.
 *
 * ```ts
 * import { LeaderboardClient } from "@openleaderboard/sdk";
 * const lb = new LeaderboardClient("https://lb.example.com", "lb_your_api_key");
 * await lb.submitScore("high", playerId, 1500);
 * const me = await lb.getRank("high", playerId);
 * const top = await lb.getTop("high", 10);
 * ```
 */
/** Thrown for non-2xx responses. */
export class LeaderboardError extends Error {
    constructor(status, message) {
        super(message);
        this.status = status;
        this.name = "LeaderboardError";
    }
}
/** Thrown when a member or board does not exist (HTTP 404). */
export class NotFoundError extends LeaderboardError {
    constructor(message) {
        super(404, message);
        this.name = "NotFoundError";
    }
}
/** Thrown when a nickname is already claimed in this app (HTTP 409). */
export class NicknameTakenError extends LeaderboardError {
    constructor(message) {
        super(409, message);
        this.name = "NicknameTakenError";
    }
}
export class LeaderboardClient {
    constructor(baseUrl, apiKey, opts = {}) {
        this.apiKey = apiKey;
        this.opts = opts;
        this.baseUrl = baseUrl.replace(/\/+$/, "");
        const f = opts.fetch ?? globalThis.fetch;
        if (!f) {
            throw new Error("global fetch unavailable; pass opts.fetch (Node <18)");
        }
        this.fetchFn = f;
    }
    /** Define a board. Typically a one-time setup call. */
    async createBoard(board, def = {}) {
        await this.send("POST", "/v1/boards", {
            board,
            sort_order: def.sortOrder,
            update_policy: def.updatePolicy,
            tie_break: def.tieBreak,
            windows: def.windows?.map((w) => ({ kind: w.kind, custom_id: w.customId })),
            approx_rank: def.approxRank,
            approx_min: def.approxMin,
            approx_max: def.approxMax,
            approx_buckets: def.approxBuckets,
        });
    }
    /** Submit a score (write-behind: durably logged, ranked shortly after). */
    async submitScore(board, member, score, opts = {}) {
        const body = {
            member,
            score,
            segments: opts.segments,
            idem: opts.idem,
            time: opts.time instanceof Date ? opts.time.toISOString() : opts.time,
        };
        if (this.opts.signingSecret) {
            const ts = Math.floor(Date.now() / 1000);
            const nonce = randomNonce();
            body.ts = ts;
            body.nonce = nonce;
            body.sig = await signSubmission(this.opts.signingSecret, this.opts.appId ?? "", board, member, score, ts, nonce);
        }
        const r = await this.send("POST", `/v1/boards/${enc(board)}/scores`, body);
        return { accepted: !!r.accepted, duplicate: !!r.duplicate };
    }
    /** A member's exact rank (O(log N)). Throws {@link NotFoundError} if absent. */
    async getRank(board, member, q = {}) {
        return this.send("GET", `/v1/boards/${enc(board)}/rank${qs({ member, ...q })}`);
    }
    /**
     * A member's approximate rank from the board's score histogram (O(buckets),
     * `exact: false`). The board must be created with `approxRank: true`; on very
     * large boards this avoids the cost of an exact rank scan. Throws
     * {@link NotFoundError} if the member is absent.
     */
    async getApproxRank(board, member, q = {}) {
        return this.send("GET", `/v1/boards/${enc(board)}/rank${qs({ member, approx: "true", ...q })}`);
    }
    /** Top N entries (rank 1..N). */
    async getTop(board, n, q = {}) {
        const r = await this.send("GET", `/v1/boards/${enc(board)}/top${qs({ n: String(n), ...q })}`);
        return r.entries ?? [];
    }
    /** A page of the ranking starting at offset (0-based). */
    async getPage(board, offset, limit, q = {}) {
        const r = await this.send("GET", `/v1/boards/${enc(board)}/page${qs({ offset: String(offset), limit: String(limit), ...q })}`);
        return r.entries ?? [];
    }
    /** The member plus up to k entries on each side of it. */
    async getNeighbors(board, member, k, q = {}) {
        const r = await this.send("GET", `/v1/boards/${enc(board)}/neighbors${qs({ member, k: String(k), ...q })}`);
        return r.entries ?? [];
    }
    /** Rank an explicit set of members against each other (a friend leaderboard). */
    async getFriends(board, members, q = {}) {
        const r = await this.send("POST", `/v1/boards/${enc(board)}/friends${qs({ ...q })}`, { members });
        return r.entries ?? [];
    }
    /**
     * Register a player: mints a `plr_...` user id and claims a nickname
     * (unique per app, case-insensitive). Submit scores with `user_id` as the
     * member; reads then include the nickname. Throws {@link NicknameTakenError}
     * if the name is claimed.
     */
    async registerUser(nickname) {
        return this.send("POST", "/v1/users", { nickname });
    }
    /** Fetch a registered player by id. Throws {@link NotFoundError} if absent. */
    async getUser(userId) {
        return this.send("GET", `/v1/users/${enc(userId)}`);
    }
    /** Resolve a nickname (case-insensitive) to its player. */
    async getUserByNickname(nickname) {
        return this.send("GET", `/v1/users${qs({ nickname })}`);
    }
    /**
     * Change a player's nickname. The user id — and therefore board data and
     * HMAC signatures — is unaffected. Throws {@link NicknameTakenError} on
     * conflict.
     */
    async renameUser(userId, nickname) {
        return this.send("PATCH", `/v1/users/${enc(userId)}`, { nickname });
    }
    /**
     * Remove a member's entry from a board — every window and segment. The
     * removal is durably logged and survives cache rebuilds. Removing an
     * absent member is a no-op; the member may submit again afterwards.
     * Throws {@link NotFoundError} for an unknown board.
     */
    async removeScore(board, member) {
        await this.send("DELETE", `/v1/boards/${enc(board)}/scores/${enc(member)}`);
    }
    /**
     * Delete a player entirely: their scores on every board in the app plus
     * their registration — the nickname is released for re-use. Works for
     * unregistered raw member ids too.
     */
    async deleteUser(userId) {
        await this.send("DELETE", `/v1/users/${enc(userId)}`);
    }
    async send(method, path, body) {
        const headers = { Authorization: `Bearer ${this.apiKey}` };
        let bodyStr;
        if (body !== undefined) {
            headers["Content-Type"] = "application/json";
            bodyStr = JSON.stringify(body);
        }
        const resp = await this.fetchFn(this.baseUrl + path, { method, headers, body: bodyStr });
        const text = await resp.text();
        if (resp.status === 404)
            throw new NotFoundError(text);
        if (resp.status === 409)
            throw new NicknameTakenError(text);
        if (!resp.ok)
            throw new LeaderboardError(resp.status, `${method} ${path} -> ${resp.status}: ${text}`);
        return text ? JSON.parse(text) : {};
    }
}
function enc(s) {
    return encodeURIComponent(s);
}
function qs(params) {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
        if (v != null && v !== "")
            p.set(k, v);
    }
    const s = p.toString();
    return s ? `?${s}` : "";
}
function randomNonce() {
    const a = new Uint8Array(16);
    globalThis.crypto.getRandomValues(a);
    return [...a].map((b) => b.toString(16).padStart(2, "0")).join("");
}
/**
 * Produces an HMAC-SHA256 submission signature matching the server (pkg/trust).
 * Exported for trusted server-side signing and testing. Matches Go's float
 * formatting for the common integer-score case. (Very large/small fractional
 * scores may format with an exponent in JS and would not match — sign integer
 * scores, or sign server-side.)
 */
export async function signSubmission(secret, app, board, member, score, ts, nonce) {
    const canonical = [app, board, member, String(score), String(ts), nonce].join("\n");
    const encb = new TextEncoder();
    const key = await globalThis.crypto.subtle.importKey("raw", encb.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
    const sig = await globalThis.crypto.subtle.sign("HMAC", key, encb.encode(canonical));
    return [...new Uint8Array(sig)].map((b) => b.toString(16).padStart(2, "0")).join("");
}
