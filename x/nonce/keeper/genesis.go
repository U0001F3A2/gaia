package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, gs *types.GenesisState) {
	if err := k.SetParams(ctx, gs.Params); err != nil {
		panic(err)
	}

	for _, entry := range gs.NonceEntries {
		addr, err := sdk.AccAddressFromBech32(entry.Address)
		if err != nil {
			panic(err)
		}
		if err := k.SetNonce(ctx, addr, entry.TimestampUs); err != nil {
			panic(err)
		}
	}
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	params, err := k.GetParams(ctx)
	if err != nil {
		panic(err)
	}

	store := k.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.NonceIteratorPrefix(), nil)
	if err != nil {
		panic(err)
	}
	defer iter.Close()

	var entries []types.NonceEntry
	prefix := types.NonceIteratorPrefix()
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		// Only process keys with our prefix
		if len(key) < len(prefix) || key[0] != prefix[0] {
			break
		}
		timestampUs, addrBytes := types.ParseNonceKey(key)
		addr := sdk.AccAddress(addrBytes).String()
		entries = append(entries, types.NonceEntry{
			TimestampUs: timestampUs,
			Address:     addr,
		})
	}

	return &types.GenesisState{
		Params:       params,
		NonceEntries: entries,
	}
}
