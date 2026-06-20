using System;
using System.Globalization;
using System.Text;
using System.Threading.Tasks;
using UnityEngine.Networking;

namespace OpenLeaderboard
{
    /// <summary>
    /// Async client for the OpenLeaderboard API. Construct once and reuse.
    ///
    /// <code>
    /// var lb = new LeaderboardClient("https://lb.example.com", "lb_yourapikey");
    /// await lb.SubmitScoreAsync("high", playerId, 1500);
    /// var me = await lb.GetRankAsync("high", playerId);
    /// var top = await lb.GetTopAsync("high", 10);
    /// </code>
    ///
    /// All methods are awaitable and run on the Unity main loop (safe to touch
    /// Unity objects in the continuation). Non-2xx responses throw
    /// <see cref="LeaderboardException"/> (or <see cref="NotFoundException"/>).
    /// </summary>
    public class LeaderboardClient
    {
        private readonly string _baseUrl;
        private readonly string _apiKey;
        private readonly string _appId;
        private readonly string _signingSecret;

        /// <param name="baseUrl">Server base URL, e.g. https://lb.example.com</param>
        /// <param name="apiKey">Per-app API key (lb_...).</param>
        /// <param name="appId">App id; required only if signing is enabled.</param>
        /// <param name="signingSecret">
        /// Optional HMAC secret. Only set this in a TRUSTED backend — never ship
        /// it inside a distributed game client.
        /// </param>
        public LeaderboardClient(string baseUrl, string apiKey, string appId = null, string signingSecret = null)
        {
            if (string.IsNullOrEmpty(baseUrl)) throw new ArgumentException("baseUrl required");
            _baseUrl = baseUrl.TrimEnd('/');
            _apiKey = apiKey;
            _appId = appId;
            _signingSecret = signingSecret;
        }

        /// <summary>Submit a score. Write-behind: it is durably logged and ranked shortly after.</summary>
        public async Task<SubmitResult> SubmitScoreAsync(string board, string member, double score,
            string[] segments = null, string idem = null, DateTime? time = null)
        {
            // Event time selects the time-window bucket (e.g. the daily board).
            // Pass the session start so a run crossing midnight counts for the day
            // it began; omit for server receive time.
            string timeStr = time.HasValue
                ? time.Value.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fff'Z'", CultureInfo.InvariantCulture)
                : "";
            var body = new SubmitRequest { member = member, score = score, segments = segments, idem = idem, time = timeStr };
            if (!string.IsNullOrEmpty(_signingSecret))
            {
                body.ts = DateTimeOffset.UtcNow.ToUnixTimeSeconds();
                body.nonce = Guid.NewGuid().ToString("N");
                body.sig = Hmac.Sign(_signingSecret, _appId, board, member, score, body.ts, body.nonce);
            }
            string resp = await SendAsync("POST", "/v1/boards/" + Esc(board) + "/scores", UnityEngine.JsonUtility.ToJson(body));
            var s = UnityEngine.JsonUtility.FromJson<SubmitResponse>(resp);
            return new SubmitResult { Accepted = s.accepted, Duplicate = s.duplicate };
        }

        /// <summary>Get a member's rank. Throws <see cref="NotFoundException"/> if absent.</summary>
        public async Task<RankEntry> GetRankAsync(string board, string member, string segment = null, string window = null)
        {
            string url = "/v1/boards/" + Esc(board) + "/rank?member=" + Esc(member) + Query(segment, window);
            string resp = await SendAsync("GET", url, null);
            return UnityEngine.JsonUtility.FromJson<RankEntry>(resp);
        }

        /// <summary>Top N entries (rank 1..N).</summary>
        public Task<RankEntry[]> GetTopAsync(string board, int n, string segment = null, string window = null)
        {
            return GetEntriesAsync("/v1/boards/" + Esc(board) + "/top?n=" + n + Query(segment, window));
        }

        /// <summary>A page of the ranking starting at offset (0-based).</summary>
        public Task<RankEntry[]> GetPageAsync(string board, int offset, int limit, string segment = null, string window = null)
        {
            return GetEntriesAsync("/v1/boards/" + Esc(board) + "/page?offset=" + offset + "&limit=" + limit + Query(segment, window));
        }

        /// <summary>The member plus up to k entries on each side of it.</summary>
        public Task<RankEntry[]> GetNeighborsAsync(string board, string member, int k, string segment = null, string window = null)
        {
            return GetEntriesAsync("/v1/boards/" + Esc(board) + "/neighbors?member=" + Esc(member) + "&k=" + k + Query(segment, window));
        }

        /// <summary>Rank an explicit set of members against each other (a friend leaderboard).</summary>
        public async Task<RankEntry[]> GetFriendsAsync(string board, string[] members, string segment = null, string window = null)
        {
            string url = "/v1/boards/" + Esc(board) + "/friends?" + Query(segment, window).TrimStart('&');
            var body = new FriendsRequest { members = members };
            string resp = await SendAsync("POST", url, UnityEngine.JsonUtility.ToJson(body));
            var e = UnityEngine.JsonUtility.FromJson<EntriesResponse>(resp);
            return e.entries ?? Array.Empty<RankEntry>();
        }

        /// <summary>Define a board. Typically a one-time dev/setup call.</summary>
        public async Task CreateBoardAsync(string board, string sortOrder = "desc", string updatePolicy = "best",
            string tieBreak = "lexical", params string[] windowKinds)
        {
            var req = new CreateBoardRequest
            {
                board = board,
                sort_order = sortOrder,
                update_policy = updatePolicy,
                tie_break = tieBreak,
            };
            if (windowKinds != null && windowKinds.Length > 0)
            {
                req.windows = new WindowDef[windowKinds.Length];
                for (int i = 0; i < windowKinds.Length; i++)
                    req.windows[i] = new WindowDef { kind = windowKinds[i] };
            }
            await SendAsync("POST", "/v1/boards", UnityEngine.JsonUtility.ToJson(req));
        }

        private async Task<RankEntry[]> GetEntriesAsync(string url)
        {
            string resp = await SendAsync("GET", url, null);
            var e = UnityEngine.JsonUtility.FromJson<EntriesResponse>(resp);
            return e.entries ?? Array.Empty<RankEntry>();
        }

        private static string Esc(string s) { return Uri.EscapeDataString(s ?? ""); }

        private static string Query(string segment, string window)
        {
            var sb = new StringBuilder();
            if (!string.IsNullOrEmpty(segment)) sb.Append("&segment=").Append(Esc(segment));
            if (!string.IsNullOrEmpty(window)) sb.Append("&window=").Append(Esc(window));
            return sb.ToString();
        }

        private async Task<string> SendAsync(string method, string path, string jsonBody)
        {
            using (var req = new UnityWebRequest(_baseUrl + path, method))
            {
                if (jsonBody != null)
                {
                    req.uploadHandler = new UploadHandlerRaw(Encoding.UTF8.GetBytes(jsonBody));
                    req.SetRequestHeader("Content-Type", "application/json");
                }
                req.downloadHandler = new DownloadHandlerBuffer();
                if (!string.IsNullOrEmpty(_apiKey))
                    req.SetRequestHeader("Authorization", "Bearer " + _apiKey);

                await req.SendWebRequest();

                if (req.result == UnityWebRequest.Result.ConnectionError ||
                    req.result == UnityWebRequest.Result.DataProcessingError)
                {
                    throw new LeaderboardException(req.responseCode, method + " " + path + ": " + req.error);
                }

                long code = req.responseCode;
                string text = req.downloadHandler != null ? req.downloadHandler.text : "";
                if (code == 404) throw new NotFoundException(text);
                if (code >= 400) throw new LeaderboardException(code, method + " " + path + " -> " + code + ": " + text);
                return text;
            }
        }
    }
}
