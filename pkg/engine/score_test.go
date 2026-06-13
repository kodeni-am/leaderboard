package engine

import (
	"errors"
	"testing"
	"time"
)

func TestScoreCodecLexicalIdentity(t *testing.T) {
	c := newScoreCodec(BoardConfig{TieBreak: TieLexical})
	for _, v := range []float64{-5, 0, 1.5, 100, 1e12} {
		enc, err := c.encode(v, time.Now())
		if err != nil || enc != v {
			t.Errorf("lexical encode(%v) = %v, %v; want identity", v, enc, err)
		}
		if got := c.decode(v); got != v {
			t.Errorf("lexical decode(%v) = %v; want identity", v, got)
		}
	}
}

func TestScoreCodecFirstToReachDescEarlierWins(t *testing.T) {
	c := newScoreCodec(BoardConfig{TieBreak: TieFirstToReach, SortOrder: SortDesc})
	epoch := c.cfg.Epoch
	early, err1 := c.encode(500, epoch.Add(1*time.Hour))
	late, err2 := c.encode(500, epoch.Add(2*time.Hour))
	if err1 != nil || err2 != nil {
		t.Fatalf("encode errors: %v %v", err1, err2)
	}
	// Descending board: the earlier achiever must have the LARGER composite so
	// ZREVRANGE ranks them first.
	if !(early > late) {
		t.Errorf("desc: earlier composite %v should exceed later %v", early, late)
	}
	// Decoding both recovers the same primary score.
	if c.decode(early) != 500 || c.decode(late) != 500 {
		t.Errorf("decode failed: %v %v", c.decode(early), c.decode(late))
	}
	// Higher primary always beats lower primary regardless of time.
	highLate, _ := c.encode(600, epoch.Add(10*time.Hour))
	if !(highLate > early) {
		t.Errorf("higher primary should dominate composite: %v vs %v", highLate, early)
	}
}

func TestScoreCodecFirstToReachAscEarlierWins(t *testing.T) {
	c := newScoreCodec(BoardConfig{TieBreak: TieFirstToReach, SortOrder: SortAsc})
	epoch := c.cfg.Epoch
	early, _ := c.encode(500, epoch.Add(1*time.Hour))
	late, _ := c.encode(500, epoch.Add(2*time.Hour))
	// Ascending board: earlier achiever must have SMALLER composite (ranks first
	// in ZRANGE).
	if !(early < late) {
		t.Errorf("asc: earlier composite %v should be below later %v", early, late)
	}
	if c.decode(early) != 500 {
		t.Errorf("decode failed: %v", c.decode(early))
	}
}

func TestScoreCodecFirstToReachRejects(t *testing.T) {
	c := newScoreCodec(BoardConfig{TieBreak: TieFirstToReach, ScoreBits: 10}) // max primary 1023
	now := c.cfg.Epoch.Add(time.Hour)
	cases := []struct {
		score float64
		tm    time.Time
	}{
		{-1, now},                        // negative
		{1.5, now},                       // non-integer
		{2000, now},                      // exceeds 2^10-1
		{5, c.cfg.Epoch.Add(-time.Hour)}, // before epoch
	}
	for _, tc := range cases {
		if _, err := c.encode(tc.score, tc.tm); !errors.Is(err, ErrScoreNotEncodable) {
			t.Errorf("encode(%v,%v) expected ErrScoreNotEncodable, got %v", tc.score, tc.tm, err)
		}
	}
}
