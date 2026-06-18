package engine

import "errors"

var (
	// ErrMemberNotFound is returned by read operations when the member has no
	// entry on the board.
	ErrMemberNotFound = errors.New("engine: member not found")

	// ErrScoreNotEncodable is returned by Submit when TieBreak=firstToReach and
	// the (score, time) pair cannot be packed into the IEEE-754 53-bit integer
	// budget without losing precision. The caller should reduce ScoreBits, the
	// score magnitude, or the timestamp range.
	ErrScoreNotEncodable = errors.New("engine: score not encodable with firstToReach tie-break")

	// ErrInvalidBoardKey is returned when a BoardKey contains characters that
	// would break the physical key structure (':', '{', '}', whitespace) or has
	// an empty App/Board.
	ErrInvalidBoardKey = errors.New("engine: invalid board key")

	// ErrInvalidConfig is returned when a BoardConfig combination is not
	// supported (e.g. UpdatePolicy=increment with TieBreak=firstToReach).
	ErrInvalidConfig = errors.New("engine: invalid board config")

	// ErrApproxDisabled is returned by GetApproxRank when the board's config does
	// not enable the approximate-rank tier (ApproxRank=false).
	ErrApproxDisabled = errors.New("engine: approximate rank not enabled for board")
)
