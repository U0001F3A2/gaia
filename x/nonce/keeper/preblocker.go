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

	// Update monotonic high watermark to prevent replay attacks if
	// past_window_us is later expanded via governance.
	existing, err := k.GetPruneWatermark(ctx)
	if err != nil {
		return err
	}
	if cutoffUs > existing {
		if err := k.SetPruneWatermark(ctx, cutoffUs); err != nil {
			return err
		}
	}

	store := k.storeService.OpenKVStore(ctx)

	// Iterate from the beginning of the nonce prefix up to (exclusive) the cutoff.
	start := types.NonceIteratorPrefix()
	end := types.BuildNoncePrefixUpTo(cutoffUs)

	// Delete in fixed-size batches to bound peak memory under high throughput.
	// Each key is ~30 bytes (prefix + timestamp + address), so 16384 keys ≈ 480 KB.
	// Sized to handle ~6000 nonces/block (1000 TPS * 6s block) in a single pass.
	// All expired nonces must be pruned to maintain consistency with the watermark;
	// partial pruning would leave nonces below the watermark, breaking the invariant.
	const batchSize = 16_384
	batch := make([][]byte, 0, batchSize)
	totalPruned := 0

	for {
		// Use closure to ensure iterator cleanup with defer
		batchEmpty, err := func() (bool, error) {
			iter, err := store.Iterator(start, end)
			if err != nil {
				return true, err
			}
			defer iter.Close()

			batch = batch[:0]
			for ; iter.Valid() && len(batch) < batchSize; iter.Next() {
				// Copy key since iterator keys may be reused after Close.
				key := iter.Key()
				keyCopy := make([]byte, len(key))
				copy(keyCopy, key)
				batch = append(batch, keyCopy)
			}
			return len(batch) == 0, nil
		}()

		if err != nil {
			return err
		}
		if batchEmpty {
			break
		}

		for _, key := range batch {
			if err := store.Delete(key); err != nil {
				return err
			}
		}
		totalPruned += len(batch)
	}

	if totalPruned > 0 {
		k.Logger(ctx).Debug("pruned expired timestamp nonces", "count", totalPruned)
	}

	return nil
}
