package types

import (
	"fmt"
)

const (
	// DefaultPastWindowUs is the default past time window in microseconds (5 minutes).
	DefaultPastWindowUs uint64 = 5 * 60 * 1_000_000

	// DefaultFutureWindowUs is the default future time window in microseconds (5 minutes).
	DefaultFutureWindowUs uint64 = 5 * 60 * 1_000_000

	// DefaultTimestampNonceCutoff is the threshold above which a sequence value
	// is treated as a timestamp nonce (2^40 = 1,099,511,627,776).
	DefaultTimestampNonceCutoff uint64 = 1 << 40
)

func DefaultParams() Params {
	return Params{
		PastWindowUs:         DefaultPastWindowUs,
		FutureWindowUs:       DefaultFutureWindowUs,
		TimestampNonceCutoff: DefaultTimestampNonceCutoff,
	}
}

func (p Params) Validate() error {
	if p.TimestampNonceCutoff == 0 {
		return fmt.Errorf("timestamp_nonce_cutoff must be > 0")
	}
	return nil
}
