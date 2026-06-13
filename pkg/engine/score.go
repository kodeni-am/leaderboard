package engine

import (
	"fmt"
	"math"
	"time"
)

// scoreCodec translates between a caller-facing primary score and the value
// actually stored in the Redis sorted set. For TieLexical the stored value is
// the raw score. For TieFirstToReach the stored value is a composite integer
// that packs the primary score in the high bits and an (inverted) timestamp in
// the low bits, so equal scores order by who reached them first.
type scoreCodec struct {
	cfg BoardConfig // assumed already withDefaults()
}

func newScoreCodec(cfg BoardConfig) scoreCodec { return scoreCodec{cfg: cfg.withDefaults()} }

// encode maps (primary score, event time) to the stored sorted-set value.
func (c scoreCodec) encode(score float64, t time.Time) (float64, error) {
	if c.cfg.TieBreak != TieFirstToReach {
		return score, nil
	}
	if score < 0 || score != math.Trunc(score) {
		return 0, fmt.Errorf("%w: requires a non-negative integer score, got %v", ErrScoreNotEncodable, score)
	}
	timeBits := 53 - c.cfg.ScoreBits
	maxPrimary := int64(1)<<c.cfg.ScoreBits - 1
	maxTime := int64(1)<<timeBits - 1
	p := int64(score)
	if p > maxPrimary {
		return 0, fmt.Errorf("%w: score %d exceeds max %d for scoreBits=%d", ErrScoreNotEncodable, p, maxPrimary, c.cfg.ScoreBits)
	}
	tsSec := int64(t.UTC().Sub(c.cfg.Epoch).Seconds())
	if tsSec < 0 || tsSec > maxTime {
		return 0, fmt.Errorf("%w: timestamp out of encodable range [0,%d]s from epoch", ErrScoreNotEncodable, maxTime)
	}
	var timeComp int64
	if c.cfg.SortOrder == SortAsc {
		timeComp = tsSec // earlier -> smaller -> ranks first in ascending order
	} else {
		timeComp = maxTime - tsSec // earlier -> larger -> ranks first in descending order
	}
	composite := (p << timeBits) | timeComp
	return float64(composite), nil
}

// decode recovers the primary score from a stored value.
func (c scoreCodec) decode(stored float64) float64 {
	if c.cfg.TieBreak != TieFirstToReach {
		return stored
	}
	timeBits := 53 - c.cfg.ScoreBits
	composite := int64(stored)
	return float64(composite >> timeBits)
}
