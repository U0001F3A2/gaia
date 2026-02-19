package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

// HasNonce checks if a timestamp nonce has been consumed for the given address.
func (k Keeper) HasNonce(ctx context.Context, addr []byte, timestampUs uint64) (bool, error) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.BuildNonceKey(timestampUs, addr)
	return store.Has(key)
}

// SetNonce marks a timestamp nonce as consumed for the given address.
func (k Keeper) SetNonce(ctx context.Context, addr []byte, timestampUs uint64) error {
	store := k.storeService.OpenKVStore(ctx)
	key := types.BuildNonceKey(timestampUs, addr)
	// Value is empty; presence of the key is sufficient.
	return store.Set(key, []byte{})
}

// ValidateAndConsumeTimestampNonce validates that a timestamp nonce is within the
// allowed time window and has not been previously consumed, then marks it as used.
// It fetches params from state. Use ValidateAndConsumeWithParams to avoid a
// redundant params read when the caller already has them (e.g. ante handler).
func (k Keeper) ValidateAndConsumeTimestampNonce(ctx context.Context, addr []byte, nonceUs uint64) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	return k.ValidateAndConsumeWithParams(ctx, addr, nonceUs, params)
}

// ValidateAndConsumeWithParams is like ValidateAndConsumeTimestampNonce but
// accepts pre-fetched params, avoiding a redundant store read.
//
// Algorithm:
// 1. Get block time in microseconds
// 2. Compute lower bound: max(0, blockTimeUs - pastWindowUs) (underflow-safe)
// 3. Compute upper bound: blockTimeUs + futureWindowUs
// 4. Reject if nonce < lower bound (expired) or nonce > upper bound (future)
// 5. Reject if already consumed (duplicate)
// 6. Consume: store nonce
func (k Keeper) ValidateAndConsumeWithParams(ctx context.Context, addr []byte, nonceUs uint64, params types.Params) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	// assumes positive blocktime. if it's ever negative, the 2^40 timestamp cutoff
	// will be infeasible anyway.
	blockTimeUs := uint64(sdkCtx.BlockTime().UnixMicro())

	// Underflow-safe lower bound
	var lowerBound uint64
	if blockTimeUs > params.PastWindowUs {
		lowerBound = blockTimeUs - params.PastWindowUs
	}

	// Enforce monotonic prune watermark: if past_window_us was expanded via
	// governance, already-pruned nonces must stay rejected.
	watermark, err := k.GetPruneWatermark(ctx)
	if err != nil {
		return err
	}
	lowerBound = max(lowerBound, watermark)

	upperBound := blockTimeUs + params.FutureWindowUs
	if upperBound < blockTimeUs {
		return errorsmod.Wrapf(types.ErrNonceOverflow, "blockTime=%d + futureWindow=%d", blockTimeUs, params.FutureWindowUs)
	}

	if nonceUs < lowerBound {
		return errorsmod.Wrapf(types.ErrNonceExpired, "nonce %d < lower bound %d", nonceUs, lowerBound)
	}
	if nonceUs > upperBound {
		return errorsmod.Wrapf(types.ErrNonceTooFarInFuture, "nonce %d > upper bound %d", nonceUs, upperBound)
	}

	has, err := k.HasNonce(ctx, addr, nonceUs)
	if err != nil {
		return err
	}
	if has {
		return types.ErrNonceDuplicate
	}

	return k.SetNonce(ctx, addr, nonceUs)
}
