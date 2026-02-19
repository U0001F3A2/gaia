// x/nonce original. Passes timestamp signer set between SigVerificationDecorator
// and IncrementSequenceDecorator via sdk.Context.WithValue (per-tx scoped).
package ante

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

type timestampSignersKeyType struct{}

var timestampSignersKey = timestampSignersKeyType{}

type TimestampSignerSet map[string]bool

func WithTimestampSigners(ctx sdk.Context, signers TimestampSignerSet) sdk.Context {
	return ctx.WithValue(timestampSignersKey, signers)
}

func GetTimestampSigners(ctx sdk.Context) TimestampSignerSet {
	v := ctx.Value(timestampSignersKey)
	if v == nil {
		return nil
	}
	return v.(TimestampSignerSet)
}
