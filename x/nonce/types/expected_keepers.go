package types

import (
	"cosmossdk.io/core/address"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

type AccountKeeper interface {
	AddressCodec() address.Codec
	GetAccount(ctx sdk.Context, addr sdk.AccAddress) sdk.AccountI
}
