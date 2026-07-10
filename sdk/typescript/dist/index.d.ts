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
export interface RankEntry {
    member: string;
    score: number;
    rank: number;
    exact: boolean;
    nickname?: string;
}
/** A registered player: server-minted id + nickname unique per app (case-insensitive). */
export interface User {
    user_id: string;
    nickname: string;
    created_at?: string;
    updated_at?: string;
}
export interface SubmitResult {
    accepted: boolean;
    duplicate: boolean;
}
/** Selects the physical board (segment/window) to read. */
export interface QueryOpts {
    segment?: string;
    /** Literal window id ("d=2026-06-13") or cadence keyword ("daily"/"weekly"/"monthly"). */
    window?: string;
}
export interface SubmitOpts {
    segments?: string[];
    idem?: string;
    /**
     * Event time of the score. Determines which time-window bucket it lands in
     * (e.g. the daily board) — set it to the session start so a run that crosses
     * midnight counts for the day it began, rather than when it was submitted.
     * Accepts a Date or an ISO-8601 string; defaults to server receive time.
     */
    time?: Date | string;
}
export interface WindowDef {
    kind: string;
    customId?: string;
}
export interface BoardDef {
    sortOrder?: "desc" | "asc";
    updatePolicy?: "best" | "last" | "increment";
    tieBreak?: "lexical" | "firstToReach";
    windows?: WindowDef[];
    /**
     * Enable the approximate-rank tier (a score histogram) so {@link
     * LeaderboardClient.getApproxRank} can estimate rank in O(buckets) on very
     * large boards. Requires `approxMax > approxMin`; `approxBuckets` defaults to 1024.
     */
    approxRank?: boolean;
    approxMin?: number;
    approxMax?: number;
    approxBuckets?: number;
}
type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;
export interface ClientOptions {
    /** App id; required only when signingSecret is set. */
    appId?: string;
    /**
     * Optional HMAC secret for signed submissions. Only use in a TRUSTED backend
     * — never ship it in browser/client code.
     */
    signingSecret?: string;
    /** Override the fetch implementation (e.g. for Node <18 or tests). */
    fetch?: FetchLike;
}
/** Thrown for non-2xx responses. */
export declare class LeaderboardError extends Error {
    readonly status: number;
    constructor(status: number, message: string);
}
/** Thrown when a member or board does not exist (HTTP 404). */
export declare class NotFoundError extends LeaderboardError {
    constructor(message: string);
}
/** Thrown when a nickname is already claimed in this app (HTTP 409). */
export declare class NicknameTakenError extends LeaderboardError {
    constructor(message: string);
}
export declare class LeaderboardClient {
    private readonly apiKey;
    private readonly opts;
    private readonly baseUrl;
    private readonly fetchFn;
    constructor(baseUrl: string, apiKey: string, opts?: ClientOptions);
    /** Define a board. Typically a one-time setup call. */
    createBoard(board: string, def?: BoardDef): Promise<void>;
    /** Submit a score (write-behind: durably logged, ranked shortly after). */
    submitScore(board: string, member: string, score: number, opts?: SubmitOpts): Promise<SubmitResult>;
    /** A member's exact rank (O(log N)). Throws {@link NotFoundError} if absent. */
    getRank(board: string, member: string, q?: QueryOpts): Promise<RankEntry>;
    /**
     * A member's approximate rank from the board's score histogram (O(buckets),
     * `exact: false`). The board must be created with `approxRank: true`; on very
     * large boards this avoids the cost of an exact rank scan. Throws
     * {@link NotFoundError} if the member is absent.
     */
    getApproxRank(board: string, member: string, q?: QueryOpts): Promise<RankEntry>;
    /** Top N entries (rank 1..N). */
    getTop(board: string, n: number, q?: QueryOpts): Promise<RankEntry[]>;
    /** A page of the ranking starting at offset (0-based). */
    getPage(board: string, offset: number, limit: number, q?: QueryOpts): Promise<RankEntry[]>;
    /** The member plus up to k entries on each side of it. */
    getNeighbors(board: string, member: string, k: number, q?: QueryOpts): Promise<RankEntry[]>;
    /** Rank an explicit set of members against each other (a friend leaderboard). */
    getFriends(board: string, members: string[], q?: QueryOpts): Promise<RankEntry[]>;
    /**
     * Register a player: mints a `plr_...` user id and claims a nickname
     * (unique per app, case-insensitive). Submit scores with `user_id` as the
     * member; reads then include the nickname. Throws {@link NicknameTakenError}
     * if the name is claimed.
     */
    registerUser(nickname: string): Promise<User>;
    /** Fetch a registered player by id. Throws {@link NotFoundError} if absent. */
    getUser(userId: string): Promise<User>;
    /** Resolve a nickname (case-insensitive) to its player. */
    getUserByNickname(nickname: string): Promise<User>;
    /**
     * Change a player's nickname. The user id — and therefore board data and
     * HMAC signatures — is unaffected. Throws {@link NicknameTakenError} on
     * conflict.
     */
    renameUser(userId: string, nickname: string): Promise<User>;
    /**
     * Remove a member's entry from a board — every window and segment. The
     * removal is durably logged and survives cache rebuilds. Removing an
     * absent member is a no-op; the member may submit again afterwards.
     * Throws {@link NotFoundError} for an unknown board.
     */
    removeScore(board: string, member: string): Promise<void>;
    /**
     * Delete a player entirely: their scores on every board in the app plus
     * their registration — the nickname is released for re-use. Works for
     * unregistered raw member ids too.
     */
    deleteUser(userId: string): Promise<void>;
    private send;
}
/**
 * Produces an HMAC-SHA256 submission signature matching the server (pkg/trust).
 * Exported for trusted server-side signing and testing. Matches Go's float
 * formatting for the common integer-score case. (Very large/small fractional
 * scores may format with an exponent in JS and would not match — sign integer
 * scores, or sign server-side.)
 */
export declare function signSubmission(secret: string, app: string, board: string, member: string, score: number, ts: number, nonce: string): Promise<string>;
export {};
