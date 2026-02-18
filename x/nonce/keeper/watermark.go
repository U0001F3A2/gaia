package keeper

import (
	"context"
	"encoding/binary"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

// GetPruneWatermark returns the highest pruning cutoff (microseconds) ever used.
// Returns 0 if no pruning has occurred yet (new chain or fresh genesis).
func (k Keeper) GetPruneWatermark(ctx context.Context) (uint64, error) {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.PruneWatermarkKey)
	if err != nil {
		return 0, err
	}
	if bz == nil {
		return 0, nil
	}
	return binary.BigEndian.Uint64(bz), nil
}

// SetPruneWatermark stores the pruning cutoff. Callers must ensure monotonicity
// (only call with a value >= the current watermark).
func (k Keeper) SetPruneWatermark(ctx context.Context, cutoffUs uint64) error {
	store := k.storeService.OpenKVStore(ctx)
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, cutoffUs)
	return store.Set(types.PruneWatermarkKey, bz)
}
