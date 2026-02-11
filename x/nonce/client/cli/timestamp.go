package cli

import (
	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const FlagTimestamp = "timestamp"

// AddTimestampFlag adds --timestamp as a persistent flag on a command.
func AddTimestampFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().Bool(FlagTimestamp, false,
		"Use auto-generated timestamp nonce for parallel tx submission (x/nonce)")
}

// TimestampAccountRetriever wraps a real AccountRetriever and overrides
// GetAccountNumberSequence to return a microsecond timestamp as the sequence.
type TimestampAccountRetriever struct {
	Inner     client.AccountRetriever
	Timestamp uint64
}

func (r *TimestampAccountRetriever) GetAccount(ctx client.Context, addr sdk.AccAddress) (client.Account, error) {
	return r.Inner.GetAccount(ctx, addr)
}

func (r *TimestampAccountRetriever) GetAccountWithHeight(ctx client.Context, addr sdk.AccAddress) (client.Account, int64, error) {
	return r.Inner.GetAccountWithHeight(ctx, addr)
}

func (r *TimestampAccountRetriever) EnsureExists(ctx client.Context, addr sdk.AccAddress) error {
	return r.Inner.EnsureExists(ctx, addr)
}

func (r *TimestampAccountRetriever) GetAccountNumberSequence(ctx client.Context, addr sdk.AccAddress) (uint64, uint64, error) {
	accNum, _, err := r.Inner.GetAccountNumberSequence(ctx, addr)
	if err != nil {
		return 0, 0, err
	}
	return accNum, r.Timestamp, nil
}
