// Forked from cosmos-sdk v0.53.4 x/auth/ante/sigverify.go
// IncrementSequenceDecorator (lines 489-527).
//
// Change from SDK original: inside the signers loop, timestamp nonce signers
// (identified via context value set by SigVerificationDecorator) skip the
// sequence increment. Everything else is identical.
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

	tsSigners := GetTimestampSigners(ctx)

	for _, signer := range signers {
		// --- BEGIN FORKED: skip timestamp nonce signers ---
		if tsSigners != nil && tsSigners[string(signer)] {
			continue
		}
		// --- END FORKED ---

		acc := isd.ak.GetAccount(ctx, signer)
		if err := acc.SetSequence(acc.GetSequence() + 1); err != nil {
			panic(err)
		}
		isd.ak.SetAccount(ctx, acc)
	}

	return next(ctx, tx, simulate)
}
