package keeper

import (
	"context"
	"encoding/binary"

	errorsmod "cosmossdk.io/errors"

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
	if len(bz) < 8 {
		return 0, errorsmod.Wrapf(types.ErrCorruptedState, "prune watermark: expected 8 bytes, got %d", len(bz))
	}
	return binary.BigEndian.Uint64(bz), nil
}

// SetPruneWatermark stores the pruning cutoff. The watermark is monotonically
// increasing: if cutoffUs <= the current watermark, the write is skipped and
// false is returned. Returns (true, nil) when the watermark actually advanced.
func (k Keeper) SetPruneWatermark(ctx context.Context, cutoffUs uint64) (bool, error) {
	existing, err := k.GetPruneWatermark(ctx)
	if err != nil {
		return false, err
	}
	if cutoffUs <= existing {
		return false, nil
	}
	store := k.storeService.OpenKVStore(ctx)
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, cutoffUs)
	return true, store.Set(types.PruneWatermarkKey, bz)
}
