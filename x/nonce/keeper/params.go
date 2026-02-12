package keeper

import (
	"context"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

// SetParams persists the module parameters to the KV store.
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := k.cdc.Marshal(&params)
	if err != nil {
		return err
	}
	return store.Set(types.ParamsKey, bz)
}

// GetParams reads the module parameters from the KV store. Returns
// DefaultParams if no params have been persisted yet.
func (k Keeper) GetParams(ctx context.Context) (types.Params, error) {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.ParamsKey)
	if err != nil {
		return types.Params{}, err
	}
	if bz == nil {
		return types.DefaultParams(), nil
	}
	var params types.Params
	err = k.cdc.Unmarshal(bz, &params)
	return params, err
}
