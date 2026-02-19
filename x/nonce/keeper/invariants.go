package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

// AllNoncesAboveWatermarkInvariant checks that every stored timestamp nonce is
// at or above the prune high watermark. If any nonce is below the watermark,
// it indicates a pruning or state-import bug.
func AllNoncesAboveWatermarkInvariant(k *Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		watermark, err := k.GetPruneWatermark(ctx)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "nonces-above-watermark",
				fmt.Sprintf("failed to get prune watermark: %v", err)), true
		}
		if watermark == 0 {
			return sdk.FormatInvariant(types.ModuleName, "nonces-above-watermark",
				"no watermark set, invariant trivially holds"), false
		}

		store := k.storeService.OpenKVStore(ctx)
		prefix := types.NonceIteratorPrefix()
		end := types.BuildNoncePrefixUpTo(watermark)

		iter, err := store.Iterator(prefix, end)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "nonces-above-watermark",
				fmt.Sprintf("failed to create iterator: %v", err)), true
		}
		defer iter.Close()

		if iter.Valid() {
			return sdk.FormatInvariant(types.ModuleName, "nonces-above-watermark",
				fmt.Sprintf("found nonce(s) below prune watermark %d", watermark)), true
		}

		return sdk.FormatInvariant(types.ModuleName, "nonces-above-watermark",
			"all nonces are at or above the prune watermark"), false
	}
}
