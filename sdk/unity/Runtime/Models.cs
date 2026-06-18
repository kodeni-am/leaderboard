using System;

// Wire DTO fields are populated by UnityEngine.JsonUtility via reflection, not
// by code, so the compiler's "never assigned" warning does not apply.
#pragma warning disable 0649

namespace OpenLeaderboard
{
    /// <summary>A member's position on a board.</summary>
    [Serializable]
    public class RankEntry
    {
        public string member;
        public double score;
        public long rank; // 1-based
        public bool exact; // false only for the sharded approximate tier
    }

    /// <summary>Outcome of a score submission.</summary>
    public class SubmitResult
    {
        public bool Accepted;  // false when deduplicated
        public bool Duplicate; // true when rejected as a duplicate idem key
    }

    /// <summary>Raised for non-2xx responses.</summary>
    public class LeaderboardException : Exception
    {
        public long StatusCode;
        public LeaderboardException(long statusCode, string message) : base(message)
        {
            StatusCode = statusCode;
        }
    }

    /// <summary>Raised when a member or board does not exist (HTTP 404).</summary>
    public class NotFoundException : LeaderboardException
    {
        public NotFoundException(string message) : base(404, message) { }
    }

    // ---- internal wire DTOs (field names match the JSON API exactly so
    // UnityEngine.JsonUtility can (de)serialize them) ----

    [Serializable]
    internal class SubmitRequest
    {
        public string member;
        public double score;
        public string[] segments;
        public string idem;
        public string sig;
        public long ts;
        public string nonce;
    }

    [Serializable]
    internal class SubmitResponse
    {
        public bool accepted;
        public bool duplicate;
    }

    [Serializable]
    internal class EntriesResponse
    {
        public RankEntry[] entries;
    }

    [Serializable]
    internal class FriendsRequest
    {
        public string[] members;
    }

    [Serializable]
    internal class WindowDef
    {
        public string kind;
        public string custom_id;
    }

    [Serializable]
    internal class CreateBoardRequest
    {
        public string board;
        public string sort_order;
        public string update_policy;
        public string tie_break;
        public WindowDef[] windows;
    }
}
