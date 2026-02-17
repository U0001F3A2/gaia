package types

import (
	"fmt"
)

const (
	// DefaultPastWindowUs is the default past time window in microseconds (5 minutes).
	DefaultPastWindowUs uint64 = 5 * 60 * 1_000_000

	// DefaultFutureWindowUs is the default future time window in microseconds (5 minutes).
	DefaultFutureWindowUs uint64 = 5 * 60 * 1_000_000

	// TimestampNonceCutoff is the fixed threshold above which a sequence value
	// is treated as a timestamp nonce (2^40 = 1,099,511,627,776).
	// Protocol constant, not governance-configurable. Matches dYdX design.
	TimestampNonceCutoff uint64 = 1 << 40

	// MaxWindowUs caps time windows at 1 hour (microseconds).
	MaxWindowUs uint64 = 60 * 60 * 1_000_000
)

func DefaultParams() Params {
	return Params{
		PastWindowUs:         DefaultPastWindowUs,
		FutureWindowUs:       DefaultFutureWindowUs,
		TimestampNonceCutoff: TimestampNonceCutoff,
	}
}

func (p Params) Validate() error {
	if p.PastWindowUs == 0 {
		return fmt.Errorf("past_window_us must be > 0")
	}
	if p.FutureWindowUs == 0 {
		return fmt.Errorf("future_window_us must be > 0")
	}
	if p.PastWindowUs > MaxWindowUs {
		return fmt.Errorf("past_window_us exceeds max (%d > %d)", p.PastWindowUs, MaxWindowUs)
	}
	if p.FutureWindowUs > MaxWindowUs {
		return fmt.Errorf("future_window_us exceeds max (%d > %d)", p.FutureWindowUs, MaxWindowUs)
	}
	if p.TimestampNonceCutoff != TimestampNonceCutoff {
		return fmt.Errorf("timestamp_nonce_cutoff must be %d (protocol constant)", TimestampNonceCutoff)
	}
	return nil
}
