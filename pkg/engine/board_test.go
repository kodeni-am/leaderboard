package engine

import (
	"testing"
	"time"
)

func TestWindowID(t *testing.T) {
	ts := time.Date(2026, 6, 13, 15, 4, 5, 0, time.UTC)
	cases := []struct {
		spec WindowSpec
		want string
	}{
		{WindowSpec{Kind: WindowAllTime}, "all"},
		{WindowSpec{}, "all"},
		{WindowSpec{Kind: WindowDaily}, "d=2026-06-13"},
		{WindowSpec{Kind: WindowWeekly}, "w=2026-W24"},
		{WindowSpec{Kind: WindowMonthly}, "m=2026-06"},
		{WindowSpec{Kind: WindowCustom, CustomID: "s=spring2026"}, "s=spring2026"},
	}
	for _, c := range cases {
		if got := c.spec.WindowID(ts); got != c.want {
			t.Errorf("WindowID(%v) = %q, want %q", c.spec, got, c.want)
		}
	}
}

func TestWindowIDUsesUTC(t *testing.T) {
	// 2026-06-13 23:30 in a +05:00 zone is 18:30 UTC same day; but 2026-06-13
	// 02:00 in +05:00 is 21:00 UTC previous day — verify we bucket by UTC.
	loc := time.FixedZone("plus5", 5*3600)
	local := time.Date(2026, 6, 13, 2, 0, 0, 0, loc) // = 2026-06-12 21:00 UTC
	if got := (WindowSpec{Kind: WindowDaily}).WindowID(local); got != "d=2026-06-12" {
		t.Errorf("expected UTC bucketing d=2026-06-12, got %q", got)
	}
}

func TestDerivePhysicalBoardsFanOut(t *testing.T) {
	lb := LogicalBoard{
		App:   "game1",
		Board: "score",
		Windows: []WindowSpec{
			{Kind: WindowAllTime},
			{Kind: WindowDaily},
		},
	}
	ev := Event{
		Member:   "p1",
		Score:    100,
		Time:     time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
		Segments: []string{"all", "region=eu"},
	}
	keys := DerivePhysicalBoards(lb, ev)
	// 2 windows x 2 segments = 4 physical boards (the write amplification).
	if len(keys) != 4 {
		t.Fatalf("expected 4 physical boards, got %d: %v", len(keys), keys)
	}
	want := map[string]bool{
		"game1:score:all:all":                false,
		"game1:score:region=eu:all":          false,
		"game1:score:all:d=2026-06-13":       false,
		"game1:score:region=eu:d=2026-06-13": false,
	}
	for _, k := range keys {
		if _, ok := want[k.String()]; !ok {
			t.Errorf("unexpected board key %q", k.String())
		}
		want[k.String()] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing expected board key %q", k)
		}
	}
}

func TestDerivePhysicalBoardsDefaults(t *testing.T) {
	lb := LogicalBoard{App: "g", Board: "b"} // no windows
	ev := Event{Member: "p", Score: 1}       // no segments
	keys := DerivePhysicalBoards(lb, ev)
	if len(keys) != 1 || keys[0].String() != "g:b:all:all" {
		t.Fatalf("expected single all:all board, got %v", keys)
	}
}

func TestBoardKeyHashTagColocation(t *testing.T) {
	k := BoardKey{App: "g", Board: "b", Segment: "all", Window: "all"}
	// All three physical keys must share the same {hash tag} so Redis Cluster
	// routes them to one slot.
	if k.zKey() != "lb:{g:b:all:all}:z" ||
		k.hKey() != "lb:{g:b:all:all}:h" ||
		k.metaKey() != "lb:{g:b:all:all}:meta" {
		t.Fatalf("unexpected keys: %s %s %s", k.zKey(), k.hKey(), k.metaKey())
	}
}

func TestBoardKeyValidate(t *testing.T) {
	bad := []BoardKey{
		{App: "", Board: "b"},
		{App: "a:b", Board: "b"},
		{App: "a", Board: "b{x}"},
		{App: "a", Board: "b", Segment: "has space"},
		{App: "a", Board: "b", Window: "w\tx"},
	}
	for _, k := range bad {
		if err := k.validate(); err == nil {
			t.Errorf("expected validation error for %+v", k)
		}
	}
	ok := BoardKey{App: "a", Board: "b", Segment: "region=eu", Window: "d=2026-06-13"}
	if err := ok.validate(); err != nil {
		t.Errorf("unexpected error for valid key: %v", err)
	}
}

func TestBoardConfigValidate(t *testing.T) {
	if err := (BoardConfig{}).validate(); err != nil {
		t.Errorf("zero config (all defaults) should be valid: %v", err)
	}
	bad := []BoardConfig{
		{SortOrder: "sideways"},
		{UpdatePolicy: "nope"},
		{TieBreak: "random"},
		{UpdatePolicy: UpdateIncrement, TieBreak: TieFirstToReach},
		{TieBreak: TieFirstToReach, ScoreBits: 60},
	}
	for _, c := range bad {
		if err := c.validate(); err == nil {
			t.Errorf("expected invalid config: %+v", c)
		}
	}
}
