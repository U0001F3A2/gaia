// Forked from cosmos-sdk v0.53.4 x/auth/ante/sigverify.go
// SigVerificationDecorator struct (lines 234-239) and AnteHandle (lines 306-415).
//
// Changes from SDK original:
//   - Struct: added NonceKeeper field; dropped unordered tx config fields.
//   - AnteHandle lines 362-369 (sequence check): replaced with threshold-based
//     routing. Sequences >= TimestampNonceCutoff go through x/nonce keeper;
//     sequences below use the standard acc.GetSequence() check.
//   - After the signer loop: timestamp signer set passed via context to
//     IncrementSequenceDecorator.
//   - verifyUnorderedNonce (lines 425-488): stubbed out; gaia does not enable
//     unordered txs.
//
// Everything else (pubkey retrieval, signature verification, signer data
// construction, unordered tx guard) is identical to the SDK original.
package ante

import (
	"fmt"

	"google.golang.org/protobuf/types/known/anypb"

	errorsmod "cosmossdk.io/errors"
	txsigning "cosmossdk.io/x/tx/signing"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authante "github.com/cosmos/cosmos-sdk/x/auth/ante"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"

	"github.com/cosmos/gaia/v26/x/nonce/keeper"
	"github.com/cosmos/gaia/v26/x/nonce/types"
)

type SigVerificationDecorator struct {
	ak              authante.AccountKeeper
	signModeHandler *txsigning.HandlerMap
	nk              *keeper.Keeper
}

func NewSigVerificationDecorator(
	ak authante.AccountKeeper,
	signModeHandler *txsigning.HandlerMap,
	nk *keeper.Keeper,
) SigVerificationDecorator {
	return SigVerificationDecorator{
		ak:              ak,
		signModeHandler: signModeHandler,
		nk:              nk,
	}
}

func (svd SigVerificationDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	sigTx, ok := tx.(authsigning.Tx)
	if !ok {
		return ctx, errorsmod.Wrap(sdkerrors.ErrTxDecode, "invalid transaction type")
	}

	utx, ok := tx.(sdk.TxWithUnordered)
	isUnordered := ok && utx.GetUnordered()
	unorderedEnabled := svd.ak.UnorderedTransactionsEnabled()

	if isUnordered && !unorderedEnabled {
		return ctx, errorsmod.Wrap(sdkerrors.ErrNotSupported, "unordered transactions are not enabled")
	}

	sigs, err := sigTx.GetSignaturesV2()
	if err != nil {
		return ctx, err
	}

	signers, err := sigTx.GetSigners()
	if err != nil {
		return ctx, err
	}

	if len(sigs) != len(signers) {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "invalid number of signer;  expected: %d, got %d", len(signers), len(sigs))
	}

	if isUnordered {
		if err := svd.verifyUnorderedNonce(ctx, utx); err != nil {
			return ctx, err
		}
	}

	var nonceParams *types.Params
	getNonceParams := func() (types.Params, error) {
		if nonceParams != nil {
			return *nonceParams, nil
		}
		p, err := svd.nk.GetParams(ctx)
		if err != nil {
			return types.Params{}, err
		}
		nonceParams = &p
		return p, nil
	}

	timestampSigners := make(TimestampSignerSet)

	for i, sig := range sigs {
		if sig.Sequence > 0 && isUnordered {
			return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "sequence is not allowed for unordered transactions")
		}
		acc, err := authante.GetSignerAcc(ctx, svd.ak, signers[i])
		if err != nil {
			return ctx, err
		}

		pubKey := acc.GetPubKey()
		if !simulate && pubKey == nil {
			return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "pubkey on account is not set")
		}

		// --- BEGIN FORKED: replaces SDK sequence check (lines 362-369) ---
		if !isUnordered {
			if sig.Sequence >= types.TimestampNonceCutoff {
				params, err := getNonceParams()
				if err != nil {
					return ctx, err
				}
				if err := svd.nk.ValidateAndConsumeWithParams(ctx, signers[i], sig.Sequence, params); err != nil {
					return ctx, err
				}

				timestampSigners[string(signers[i])] = true
			} else {
				if sig.Sequence != acc.GetSequence() {
					return ctx, errorsmod.Wrapf(
						sdkerrors.ErrWrongSequence,
						"account sequence mismatch, expected %d, got %d", acc.GetSequence(), sig.Sequence,
					)
				}
			}
		}
		// --- END FORKED ---

		genesis := ctx.BlockHeight() == 0
		chainID := ctx.ChainID()
		var accNum uint64
		if !genesis {
			accNum = acc.GetAccountNumber()
		}

		if !simulate && !ctx.IsReCheckTx() && ctx.IsSigverifyTx() {
			anyPk, _ := codectypes.NewAnyWithValue(pubKey)

			signerData := txsigning.SignerData{
				Address:       acc.GetAddress().String(),
				ChainID:       chainID,
				AccountNumber: accNum,
				Sequence:      sig.Sequence,
				PubKey: &anypb.Any{
					TypeUrl: anyPk.TypeUrl,
					Value:   anyPk.Value,
				},
			}
			adaptableTx, ok := tx.(authsigning.V2AdaptableTx)
			if !ok {
				return ctx, fmt.Errorf("expected tx to implement V2AdaptableTx, got %T", tx)
			}
			txData := adaptableTx.GetSigningTxData()
			err = authsigning.VerifySignature(ctx, pubKey, signerData, sig.Data, svd.signModeHandler, txData)
			if err != nil {
				var errMsg string
				if authante.OnlyLegacyAminoSigners(sig.Data) {
					errMsg = fmt.Sprintf("signature verification failed; please verify account number (%d), sequence (%d) and chain-id (%s)", accNum, acc.GetSequence(), chainID)
				} else {
					errMsg = fmt.Sprintf("signature verification failed; please verify account number (%d) and chain-id (%s): (%s)", accNum, chainID, err.Error())
				}
				return ctx, errorsmod.Wrap(sdkerrors.ErrUnauthorized, errMsg)
			}
		}
	}

	if len(timestampSigners) > 0 {
		ctx = WithTimestampSigners(ctx, timestampSigners)
	}

	return next(ctx, tx, simulate)
}

func (svd SigVerificationDecorator) verifyUnorderedNonce(_ sdk.Context, _ sdk.TxWithUnordered) error {
	return errorsmod.Wrap(sdkerrors.ErrNotSupported, "unordered transactions are not supported with x/nonce ante handler")
}
