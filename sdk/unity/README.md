# OpenLeaderboard — Unity SDK

A lightweight C# client for the [OpenLeaderboard](../../README.md) API. Submit
scores and query ranks, top-N, neighbors, and friend leaderboards from Unity.

- **Async/await** over `UnityWebRequest` — works on all targets including
  **WebGL** and **IL2CPP** (no threads, no `HttpClient`).
- **Zero dependencies** — uses Unity's built-in `JsonUtility`.
- Unity **2020.3+**.

## Install

**Via Package Manager (git URL):** Add to `Packages/manifest.json`:

```json
"com.openleaderboard.sdk": "https://github.com/kodeni-am/leaderboard.git?path=/sdk/unity"
```

**Or** copy `sdk/unity/Runtime` into your project's `Assets/`.

## Quickstart

```csharp
using OpenLeaderboard;

var lb = new LeaderboardClient("https://lb.example.com", "lb_your_api_key");

// Submit (write-behind: durably logged, ranked shortly after).
await lb.SubmitScoreAsync("high", playerId, 1500);

// Read back.
RankEntry me   = await lb.GetRankAsync("high", playerId);   // throws NotFoundException if absent
RankEntry[] top = await lb.GetTopAsync("high", 10);
RankEntry[] near = await lb.GetNeighborsAsync("high", playerId, 5);   // me ± 5
RankEntry[] friends = await lb.GetFriendsAsync("high", new[] { "alice", "bob" });
```

Segmented / time-windowed reads use the optional `segment` and `window` args
(`window` accepts a literal id like `d=2026-06-13` or a keyword `daily`/`weekly`/`monthly`):

```csharp
await lb.GetTopAsync("high", 10, segment: "region=eu", window: "daily");
```

Errors surface as exceptions: `NotFoundException` (404) and `LeaderboardException`
(other non-2xx, with `.StatusCode`).

A runnable example is in `Samples~/Basic/LeaderboardDemo.cs` (import via the
Package Manager "Samples" section).

## Security note on signed submissions

The client supports HMAC-signed submissions (`new LeaderboardClient(url, key, appId, signingSecret)`),
matching the server's `SIGNING_SECRET` verification. **Do not embed the signing
secret in a distributed game build** — anyone can extract it. Signing is meant
for a *trusted backend* that submits on players' behalf. For untrusted clients,
rely on per-app API keys and server-side validation instead. (Integer scores
sign identically across this SDK and the Go server; signing has been
cross-validated against the server and `openssl`.)
