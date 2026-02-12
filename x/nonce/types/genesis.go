package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params:       DefaultParams(),
		NonceEntries: []NonceEntry{},
	}
}

func ValidateGenesis(gs *GenesisState) error {
	if err := gs.Params.Validate(); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	seen := make(map[string]struct{})
	for _, entry := range gs.NonceEntries {
		if entry.Address == "" {
			return fmt.Errorf("nonce entry has empty address")
		}
		if _, err := sdk.AccAddressFromBech32(entry.Address); err != nil {
			return fmt.Errorf("invalid nonce entry address %q: %w", entry.Address, err)
		}
		if entry.TimestampUs == 0 {
			return fmt.Errorf("nonce entry has zero timestamp")
		}
		key := fmt.Sprintf("%d/%s", entry.TimestampUs, entry.Address)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate nonce entry: %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}
