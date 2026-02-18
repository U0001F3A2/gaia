package keeper

import (
	"context"
	"encoding/binary"
	"fmt"

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
		return 0, fmt.Errorf("corrupted prune watermark: expected 8 bytes, got %d", len(bz))
	}
	return binary.BigEndian.Uint64(bz), nil
}

// SetPruneWatermark stores the pruning cutoff. The watermark is monotonically
// increasing: attempts to set a value lower than the current watermark return
// an error to prevent replay of already-pruned nonces.
func (k Keeper) SetPruneWatermark(ctx context.Context, cutoffUs uint64) error {
	existing, err := k.GetPruneWatermark(ctx)
	if err != nil {
		return err
	}
	if cutoffUs < existing {
		return fmt.Errorf("prune watermark regression: new %d < existing %d", cutoffUs, existing)
	}
	store := k.storeService.OpenKVStore(ctx)
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, cutoffUs)
	return store.Set(types.PruneWatermarkKey, bz)
}
