package engine

import (
	"fmt"
	"strings"
	"time"
)

// SortOrder controls whether higher or lower scores rank first.
type SortOrder string

const (
	// SortDesc ranks higher scores first (default; e.g. points, kills).
	SortDesc SortOrder = "desc"
	// SortAsc ranks lower scores first (e.g. race/lap times).
	SortAsc SortOrder = "asc"
)

// UpdatePolicy controls how a new submission combines with an existing score.
type UpdatePolicy string

const (
	// UpdateBest keeps the better score (atomic ZADD GT/LT). Most common.
	UpdateBest UpdatePolicy = "best"
	// UpdateLast overwrites with the latest submitted score.
	UpdateLast UpdatePolicy = "last"
	// UpdateIncrement adds the submitted value to the existing score (ZINCRBY).
	// Requires TieBreak=lexical (composite encoding is incompatible with sums).
	UpdateIncrement UpdatePolicy = "increment"
)

// TieBreak controls ordering among members with equal primary scores.
type TieBreak string

const (
	// TieLexical breaks score ties by member id (Redis default). Deterministic.
	// Direction follows the board: ascending boards order ties by member
	// ascending; descending boards order ties by member descending (ZREVRANGE
	// reverses the lexical order). Use TieFirstToReach when tie order must be
	// time-based rather than id-based.
	TieLexical TieBreak = "lexical"
	// TieFirstToReach ranks the earlier achiever first among equal scores by
	// packing an inverted timestamp into the score's low bits.
	TieFirstToReach TieBreak = "firstToReach"
)

// BoardConfig is the per-logical-board semantic configuration. It is required
// by every engine operation because rank direction and score decoding depend
// on it.
type BoardConfig struct {
	SortOrder    SortOrder    `json:"sort_order"`
	UpdatePolicy UpdatePolicy `json:"update_policy"`
	TieBreak     TieBreak     `json:"tie_break"`
	// ScoreBits reserves N high bits for the primary score when
	// TieBreak=firstToReach. Remaining (53-ScoreBits) bits encode time in
	// seconds since Epoch. Ignored for TieLexical. Default 20 (max score
	// 1,048,575; ~272 years of timestamp range).
	ScoreBits uint `json:"score_bits,omitempty"`
	// Epoch is the base time for firstToReach timestamp encoding. Zero value
	// means 2020-01-01 UTC.
	Epoch time.Time `json:"epoch,omitempty"`

	// ApproxRank enables the approximate-rank read tier: on every write the
	// engine maintains a fixed-bucket score histogram (the board's :h key) so a
	// member's global rank can be estimated in O(buckets) without scanning the
	// set. It is the building block the sharded engine uses for global rank
	// across shards; on a single set it is opt-in (GetRank stays exact and
	// O(log N)). Requires ApproxMax > ApproxMin.
	ApproxRank bool `json:"approx_rank,omitempty"`
	// ApproxMin and ApproxMax bound the primary-score range the histogram
	// buckets span. Scores outside the range clamp to the edge buckets.
	ApproxMin float64 `json:"approx_min,omitempty"`
	ApproxMax float64 `json:"approx_max,omitempty"`
	// ApproxBuckets is the number of equal-width histogram bins; rank resolution
	// is one bucket width. Default 1024.
	ApproxBuckets int `json:"approx_buckets,omitempty"`
}

// withDefaults returns a copy with zero-value fields filled in.
func (c BoardConfig) withDefaults() BoardConfig {
	if c.SortOrder == "" {
		c.SortOrder = SortDesc
	}
	if c.UpdatePolicy == "" {
		c.UpdatePolicy = UpdateBest
	}
	if c.TieBreak == "" {
		c.TieBreak = TieLexical
	}
	if c.ScoreBits == 0 {
		c.ScoreBits = 20
	}
	if c.Epoch.IsZero() {
		c.Epoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if c.ApproxRank && c.ApproxBuckets == 0 {
		c.ApproxBuckets = 1024
	}
	return c
}

// Validate reports whether the config is internally consistent. Exported for
// callers (e.g. the API) that validate board definitions before persisting.
func (c BoardConfig) Validate() error { return c.validate() }

func (c BoardConfig) validate() error {
	cc := c.withDefaults()
	if cc.SortOrder != SortDesc && cc.SortOrder != SortAsc {
		return fmt.Errorf("%w: sortOrder %q", ErrInvalidConfig, c.SortOrder)
	}
	switch cc.UpdatePolicy {
	case UpdateBest, UpdateLast, UpdateIncrement:
	default:
		return fmt.Errorf("%w: updatePolicy %q", ErrInvalidConfig, c.UpdatePolicy)
	}
	if cc.TieBreak != TieLexical && cc.TieBreak != TieFirstToReach {
		return fmt.Errorf("%w: tieBreak %q", ErrInvalidConfig, c.TieBreak)
	}
	if cc.UpdatePolicy == UpdateIncrement && cc.TieBreak == TieFirstToReach {
		return fmt.Errorf("%w: increment requires lexical tie-break", ErrInvalidConfig)
	}
	if cc.TieBreak == TieFirstToReach && (cc.ScoreBits < 1 || cc.ScoreBits > 52) {
		return fmt.Errorf("%w: scoreBits must be 1..52, got %d", ErrInvalidConfig, cc.ScoreBits)
	}
	if cc.ApproxRank {
		if !(cc.ApproxMax > cc.ApproxMin) {
			return fmt.Errorf("%w: approxRank requires approxMax > approxMin (got min=%v max=%v)", ErrInvalidConfig, cc.ApproxMin, cc.ApproxMax)
		}
		if cc.ApproxBuckets < 1 {
			return fmt.Errorf("%w: approxBuckets must be >= 1, got %d", ErrInvalidConfig, cc.ApproxBuckets)
		}
	}
	return nil
}

// BoardKey is the physical address of a single sorted set. App+Board identify
// the logical board; Segment and Window slice it into physical sets.
type BoardKey struct {
	App     string `json:"app"`
	Board   string `json:"board"`
	Segment string `json:"segment,omitempty"` // default "all"
	Window  string `json:"window,omitempty"`  // default "all"
}

func sanitizeSegment(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

// invalid reports whether a key component contains structural characters.
func invalidComponent(s string) bool {
	return strings.ContainsAny(s, ":{}\t\n\r ") || s == ""
}

// Validate reports whether the key components are structurally safe. Exported
// for callers that validate board addresses before use.
func (k BoardKey) Validate() error { return k.validate() }

func (k BoardKey) validate() error {
	seg := sanitizeSegment(k.Segment)
	win := k.Window
	if win == "" {
		win = "all"
	}
	if invalidComponent(k.App) || invalidComponent(k.Board) ||
		invalidComponent(seg) || invalidComponent(win) {
		return fmt.Errorf("%w: %+v", ErrInvalidBoardKey, k)
	}
	return nil
}

// hashTag is the substring Redis Cluster hashes (between braces), guaranteeing
// all of a board's keys share a slot.
func (k BoardKey) hashTag() string {
	seg := sanitizeSegment(k.Segment)
	win := k.Window
	if win == "" {
		win = "all"
	}
	return fmt.Sprintf("%s:%s:%s:%s", k.App, k.Board, seg, win)
}

func (k BoardKey) zKey() string    { return "lb:{" + k.hashTag() + "}:z" }
func (k BoardKey) hKey() string    { return "lb:{" + k.hashTag() + "}:h" }
func (k BoardKey) metaKey() string { return "lb:{" + k.hashTag() + "}:meta" }

// String renders a stable human-readable identifier for the physical board.
func (k BoardKey) String() string { return k.hashTag() }

// WindowKind enumerates the temporal bucketing cadences.
type WindowKind string

const (
	WindowAllTime WindowKind = "all"
	WindowDaily   WindowKind = "daily"
	WindowWeekly  WindowKind = "weekly"
	WindowMonthly WindowKind = "monthly"
	// WindowCustom uses a caller-supplied id (e.g. seasonal: "s=spring2026").
	WindowCustom WindowKind = "custom"
)

// WindowSpec describes one temporal dimension of a logical board.
type WindowSpec struct {
	Kind WindowKind `json:"kind"`
	// CustomID is used only when Kind==WindowCustom.
	CustomID string `json:"custom_id,omitempty"`
}

// WindowID returns the concrete window bucket id for a given event time (UTC).
func (w WindowSpec) WindowID(t time.Time) string {
	t = t.UTC()
	switch w.Kind {
	case WindowAllTime, "":
		return "all"
	case WindowDaily:
		return "d=" + t.Format("2006-01-02")
	case WindowWeekly:
		y, wk := t.ISOWeek()
		return fmt.Sprintf("w=%04d-W%02d", y, wk)
	case WindowMonthly:
		return "m=" + t.Format("2006-01")
	case WindowCustom:
		return w.CustomID
	default:
		return "all"
	}
}

// LogicalBoard is the developer-facing board definition. A single score event
// fans out to len(Windows) x len(event.Segments) physical boards.
type LogicalBoard struct {
	App     string       `json:"app"`
	Board   string       `json:"board"`
	Config  BoardConfig  `json:"config"`
	Windows []WindowSpec `json:"windows"` // at least one; defaults to [{WindowAllTime}]
}

// Event is a single score submission with the context needed to derive which
// physical boards it touches.
type Event struct {
	Member   string
	Score    float64
	Time     time.Time
	Segments []string // concrete segments; defaults to ["all"]
}

// DerivePhysicalBoards returns every physical BoardKey a score event must be
// written to. This is the single source of write-amplification: the result
// length equals the number of Redis writes the fan-out will perform.
func DerivePhysicalBoards(lb LogicalBoard, ev Event) []BoardKey {
	windows := lb.Windows
	if len(windows) == 0 {
		windows = []WindowSpec{{Kind: WindowAllTime}}
	}
	segs := ev.Segments
	if len(segs) == 0 {
		segs = []string{"all"}
	}
	keys := make([]BoardKey, 0, len(windows)*len(segs))
	for _, w := range windows {
		win := w.WindowID(ev.Time)
		for _, s := range segs {
			keys = append(keys, BoardKey{
				App:     lb.App,
				Board:   lb.Board,
				Segment: sanitizeSegment(s),
				Window:  win,
			})
		}
	}
	return keys
}
