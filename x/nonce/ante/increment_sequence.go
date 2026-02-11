// IncrementSequenceDecorator is forked from cosmos-sdk/x/auth/ante/sigverify.go
// (SDK v0.53.4, lines 489-527). The only change: signers that used timestamp nonces
// (tracked via context value from SigVerificationDecorator) skip sequence increment,
// since their nonce was already consumed in the nonce KeySet.
package ante

import (
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authante "github.com/cosmos/cosmos-sdk/x/auth/ante"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
)

type IncrementSequenceDecorator struct {
	ak authante.AccountKeeper
}

func NewIncrementSequenceDecorator(ak authante.AccountKeeper) IncrementSequenceDecorator {
	return IncrementSequenceDecorator{
		ak: ak,
	}
}

func (isd IncrementSequenceDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	// Unordered tx handling (identical to SDK)
	if utx, ok := tx.(sdk.TxWithUnordered); ok && utx.GetUnordered() {
		if !isd.ak.UnorderedTransactionsEnabled() {
			return ctx, errorsmod.Wrap(sdkerrors.ErrNotSupported, "unordered transactions are disabled")
		}
		return next(ctx, tx, simulate)
	}

	sigTx, ok := tx.(authsigning.SigVerifiableTx)
	if !ok {
		return ctx, errorsmod.Wrap(sdkerrors.ErrTxDecode, "invalid transaction type")
	}

	signers, err := sigTx.GetSigners()
	if err != nil {
		return sdk.Context{}, err
	}

	// Get timestamp signers set from context (set by SigVerificationDecorator)
	tsSigners := GetTimestampSigners(ctx)

	for _, signer := range signers {
		// --- BEGIN FORKED SECTION ---
		// Skip sequence increment for timestamp nonce signers.
		// Their nonce was consumed in the nonce KeySet by SigVerificationDecorator.
		if tsSigners != nil && tsSigners[string(signer)] {
			continue
		}
		// --- END FORKED SECTION ---

		acc := isd.ak.GetAccount(ctx, signer)
		if err := acc.SetSequence(acc.GetSequence() + 1); err != nil {
			panic(err)
		}
		isd.ak.SetAccount(ctx, acc)
	}

	return next(ctx, tx, simulate)
}
