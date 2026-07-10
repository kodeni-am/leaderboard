// Package engine is the core ranking engine for OpenLeaderboard (SP1).
//
// It is a pure library over a Redis-compatible server. It has no knowledge of
// HTTP, AWS, durable logs, or tenancy — it provides the ranking primitives that
// the rest of the system composes. Rank reads are O(log N) regardless of board
// size; the engine relies on Redis sorted sets, whose rank is intrinsic to the
// data structure.
package engine

import (
	"context"
	"time"
)

// Board is a physical board address paired with its semantic config. Every
// operation needs the config because rank direction and score decoding depend
// on it.
type Board struct {
	Key    BoardKey
	Config BoardConfig
}

func (b Board) validate() error {
	if err := b.Key.validate(); err != nil {
		return err
	}
	return b.Config.validate()
}

// RankEntry is a member's position on a board.
type RankEntry struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"` // decoded primary score
	Rank   int64   `json:"rank"`  // 1-based
	Exact  bool    `json:"exact"` // false only for the sharded approximate tier
	// Nickname is a friendly display name attached by the API layer from the
	// per-app player registry; the engine itself never populates it.
	Nickname string `json:"nickname,omitempty"`
}

// SubmitResult reports the outcome of a write.
type SubmitResult struct {
	Updated bool    `json:"updated"` // whether the stored value changed
	Score   float64 `json:"score"`   // decoded primary score now stored
}

// SubmitOp is one entry in a batched/pipelined write (used by the SP2 fan-out).
type SubmitOp struct {
	Board  Board
	Member string
	Score  float64
	Time   time.Time
}

// RankingEngine is the contract every backend implements. The Redis backend is
// the v1 implementation; the interface is intentionally backend-agnostic so a
// sharded/custom engine can be substituted later.
type RankingEngine interface {
	Submit(ctx context.Context, b Board, member string, score float64, t time.Time) (SubmitResult, error)
	SubmitBatch(ctx context.Context, ops []SubmitOp) ([]SubmitResult, error)
	GetRank(ctx context.Context, b Board, member string) (RankEntry, error)
	// GetApproxRank estimates a member's rank from the board's score histogram
	// (Exact=false). Returns ErrApproxDisabled unless the board enables it.
	GetApproxRank(ctx context.Context, b Board, member string) (RankEntry, error)
	TopN(ctx context.Context, b Board, n int) ([]RankEntry, error)
	Page(ctx context.Context, b Board, offset, limit int) ([]RankEntry, error)
	Neighbors(ctx context.Context, b Board, member string, k int) ([]RankEntry, error)
	FriendRank(ctx context.Context, b Board, members []string) ([]RankEntry, error)
	Count(ctx context.Context, b Board) (int64, error)
	Remove(ctx context.Context, b Board, member string) error
	// RemoveFromAll removes member from every live physical board of the
	// logical board — all segments and all windows currently in the cache,
	// including past windows the reaper has not yet expired. Approx-board
	// histograms are maintained. Removing an absent member is a no-op.
	RemoveFromAll(ctx context.Context, lb LogicalBoard, member string) error
	// Segments returns the deduplicated, lexically sorted segment names that
	// currently have live physical boards for lb — including "all", where
	// unsegmented submits land. An empty board yields an empty non-nil slice.
	Segments(ctx context.Context, lb LogicalBoard) ([]string, error)
	Reset(ctx context.Context, b Board) error
}
