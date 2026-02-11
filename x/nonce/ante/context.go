package ante

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// timestampSignersKey is a context value key for passing the set of signers
// that used timestamp nonces from SigVerificationDecorator to IncrementSequenceDecorator.
type timestampSignersKeyType struct{}

var timestampSignersKey = timestampSignersKeyType{}

// TimestampSignerSet is a set of signer addresses (as strings) that used timestamp nonces.
type TimestampSignerSet map[string]bool

// WithTimestampSigners stores the set of timestamp nonce signers in the context.
func WithTimestampSigners(ctx sdk.Context, signers TimestampSignerSet) sdk.Context {
	return ctx.WithValue(timestampSignersKey, signers)
}

// GetTimestampSigners retrieves the set of timestamp nonce signers from the context.
// Returns nil if not set.
func GetTimestampSigners(ctx sdk.Context) TimestampSignerSet {
	v := ctx.Value(timestampSignersKey)
	if v == nil {
		return nil
	}
	return v.(TimestampSignerSet)
}
