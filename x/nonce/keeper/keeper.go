package keeper

import (
	"context"

	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

// Keeper manages the x/nonce module state: timestamp nonce storage, params,
// and expired-nonce pruning. It stores consumed timestamp nonces in a global
// KeySet keyed by (timestamp_us, address) for O(1) duplicate detection.
type Keeper struct {
	storeService storetypes.KVStoreService
	cdc          codec.BinaryCodec
	authority    string
}

// NewKeeper returns a new x/nonce Keeper. The authority address is the account
// permitted to execute MsgUpdateParams (typically x/gov module account).
func NewKeeper(
	cdc codec.BinaryCodec,
	storeService storetypes.KVStoreService,
	authority string,
) *Keeper {
	return &Keeper{
		storeService: storeService,
		cdc:          cdc,
		authority:    authority,
	}
}

// Logger returns a module-scoped logger for x/nonce.
func (k Keeper) Logger(ctx context.Context) log.Logger {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return sdkCtx.Logger().With("module", "x/"+types.ModuleName)
}

// GetAuthority returns the address authorized to execute governance messages
// for this module.
func (k Keeper) GetAuthority() string {
	return k.authority
}
