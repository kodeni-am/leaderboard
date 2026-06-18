using System;
using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace OpenLeaderboard
{
    /// <summary>
    /// HMAC-SHA256 submission signing, matching the server's canonical message
    /// (pkg/trust). NOTE: the shared secret must never ship inside a game
    /// client binary — signing is for a trusted backend that submits on behalf
    /// of players. Integer scores (the common case) sign identically across
    /// Go and C#; see FormatScore for fractional-score caveats.
    /// </summary>
    internal static class Hmac
    {
        public static string Sign(string secret, string app, string board, string member,
            double score, long ts, string nonce)
        {
            string canonical = string.Join("\n",
                app ?? "",
                board ?? "",
                member ?? "",
                FormatScore(score),
                ts.ToString(CultureInfo.InvariantCulture),
                nonce ?? "");

            using (var mac = new HMACSHA256(Encoding.UTF8.GetBytes(secret)))
            {
                byte[] hash = mac.ComputeHash(Encoding.UTF8.GetBytes(canonical));
                var sb = new StringBuilder(hash.Length * 2);
                foreach (byte b in hash) sb.Append(b.ToString("x2", CultureInfo.InvariantCulture));
                return sb.ToString();
            }
        }

        /// <summary>
        /// Matches Go's strconv.FormatFloat(score, 'f', -1, 64): shortest
        /// decimal, never exponential. Integer-valued scores format with no
        /// decimal point ("500"), which is guaranteed to match Go.
        /// </summary>
        internal static string FormatScore(double score)
        {
            if (score == Math.Truncate(score) && Math.Abs(score) < 1e15)
            {
                return ((long)score).ToString(CultureInfo.InvariantCulture);
            }
            // Round-trippable shortest form for fractional scores.
            return score.ToString("R", CultureInfo.InvariantCulture);
        }
    }
}
