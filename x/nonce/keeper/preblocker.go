package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

// PruneExpiredNonces removes all timestamp nonces older than (blockTime - pastWindow).
// Called in PreBlocker to keep state bounded by time window rather than count.
//
// Iteration strategy: since keys are prefixed with NonceKeyPrefix and then the
// timestamp in big-endian, iterating from NonceKeyPrefix to the cutoff timestamp
// yields all expired entries in order.
func (k Keeper) PruneExpiredNonces(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	blockTimeUs := uint64(sdkCtx.BlockTime().UnixMicro())

	// Underflow-safe cutoff
	var cutoffUs uint64
	if blockTimeUs > params.PastWindowUs {
		cutoffUs = blockTimeUs - params.PastWindowUs
	}

	if cutoffUs == 0 {
		return nil
	}

	store := k.storeService.OpenKVStore(ctx)

	// Iterate from the beginning of the nonce prefix up to (exclusive) the cutoff.
	start := types.NonceIteratorPrefix()
	end := types.BuildNoncePrefixUpTo(cutoffUs)

	iter, err := store.Iterator(start, end)
	if err != nil {
		return err
	}
	defer iter.Close()

	// Collect keys to delete (can't delete during iteration on some backends).
	var keysToDelete [][]byte
	for ; iter.Valid(); iter.Next() {
		keysToDelete = append(keysToDelete, iter.Key())
	}

	for _, key := range keysToDelete {
		if err := store.Delete(key); err != nil {
			return err
		}
	}

	if len(keysToDelete) > 0 {
		k.Logger(ctx).Debug("pruned expired timestamp nonces", "count", len(keysToDelete))
	}

	return nil
}
