using UnityEngine;

namespace OpenLeaderboard.Samples
{
    /// <summary>
    /// Minimal end-to-end demo: submit a score, then read back rank and top-N.
    /// Drop this on a GameObject and fill in the URL + API key in the inspector.
    /// </summary>
    public class LeaderboardDemo : MonoBehaviour
    {
        [SerializeField] private string baseUrl = "http://localhost:8080";
        [SerializeField] private string apiKey = "lb_your_api_key";
        [SerializeField] private string board = "high";
        [SerializeField] private string playerId = "player-1";

        private LeaderboardClient _client;

        private async void Start()
        {
            _client = new LeaderboardClient(baseUrl, apiKey);
            try
            {
                var result = await _client.SubmitScoreAsync(board, playerId, Random.Range(0, 10000));
                Debug.Log("Submitted (accepted=" + result.Accepted + ")");

                // Write-behind: the score is applied asynchronously, so a rank
                // read immediately after a submit may briefly 404.
                var me = await _client.GetRankAsync(board, playerId);
                Debug.Log("You are rank " + me.rank + " with " + me.score);

                var top = await _client.GetTopAsync(board, 10);
                foreach (var e in top)
                    Debug.Log("#" + e.rank + " " + e.member + " = " + e.score);
            }
            catch (NotFoundException)
            {
                Debug.Log("Not on the board yet — try again shortly (write-behind).");
            }
            catch (LeaderboardException ex)
            {
                Debug.LogError("Leaderboard error " + ex.StatusCode + ": " + ex.Message);
            }
        }
    }
}
