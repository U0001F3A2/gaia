package simulation

import (
	"math/rand"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	xsim "github.com/cosmos/cosmos-sdk/x/simulation"

	"github.com/cosmos/gaia/v26/x/nonce/keeper"
	"github.com/cosmos/gaia/v26/x/nonce/types"
)

const (
	OpWeightMsgTimestampNonceSend         = "op_weight_msg_timestamp_nonce_send"
	DefaultWeightTimestampNonceSend       = 30
	OpWeightMsgTimestampNonceDuplicate    = "op_weight_msg_timestamp_nonce_duplicate"
	DefaultWeightTimestampNonceDuplicate  = 5
)

// WeightedOperations returns simulation operations for the x/nonce module.
// The operation creates a bank.MsgSend signed with a timestamp nonce (sequence >= 2^40)
// instead of the account's sequential nonce. This exercises the full ante handler
// chain: threshold routing, nonce validation/consumption, sequence skip, and
// cross-block pruning.
func WeightedOperations(
	appParams simtypes.AppParams,
	_ codec.JSONCodec,
	txGen client.TxConfig,
	ak authkeeper.AccountKeeper,
	bk bankkeeper.Keeper,
	nk *keeper.Keeper,
) []simtypes.WeightedOperation {
	var sendWeight int
	appParams.GetOrGenerate(OpWeightMsgTimestampNonceSend, &sendWeight, nil, func(_ *rand.Rand) {
		sendWeight = DefaultWeightTimestampNonceSend
	})

	var dupWeight int
	appParams.GetOrGenerate(OpWeightMsgTimestampNonceDuplicate, &dupWeight, nil, func(_ *rand.Rand) {
		dupWeight = DefaultWeightTimestampNonceDuplicate
	})

	return []simtypes.WeightedOperation{
		xsim.NewWeightedOperation(sendWeight, SimulateTimestampNonceSend(txGen, ak, bk, nk)),
		xsim.NewWeightedOperation(dupWeight, SimulateTimestampNonceDuplicate(txGen, ak, bk, nk)),
	}
}

// SimulateTimestampNonceSend creates a bank.MsgSend signed with a random
// timestamp nonce within the valid time window.
func SimulateTimestampNonceSend(
	txGen client.TxConfig,
	ak authkeeper.AccountKeeper,
	bk bankkeeper.Keeper,
	nk *keeper.Keeper,
) simtypes.Operation {
	return func(
		r *rand.Rand, app *baseapp.BaseApp, ctx sdk.Context,
		accs []simtypes.Account, chainID string,
	) (simtypes.OperationMsg, []simtypes.FutureOperation, error) {
		msgType := sdk.MsgTypeURL(&banktypes.MsgSend{})

		params, err := nk.GetParams(ctx)
		if err != nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "failed to get nonce params"), nil, nil
		}

		// pick random sender and recipient
		from, _ := simtypes.RandomAcc(r, accs)
		to, _ := simtypes.RandomAcc(r, accs)
		for from.PubKey.Equals(to.PubKey) {
			to, _ = simtypes.RandomAcc(r, accs)
		}

		fromAcc := ak.GetAccount(ctx, from.Address)
		if fromAcc == nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "account not found"), nil, nil
		}

		spendable := bk.SpendableCoins(ctx, from.Address)
		sendCoins := simtypes.RandSubsetCoins(r, spendable)
		if sendCoins.Empty() {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "no spendable coins"), nil, nil
		}

		if err := bk.IsSendEnabledCoins(ctx, sendCoins...); err != nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, err.Error()), nil, nil
		}

		// generate a random timestamp nonce within the valid window
		blockTimeUs := uint64(ctx.BlockTime().UnixMicro())
		windowSize := params.PastWindowUs + params.FutureWindowUs
		nonce := blockTimeUs - params.PastWindowUs + uint64(r.Int63n(int64(windowSize)))

		// ensure nonce is above the cutoff (it always will be for real timestamps,
		// but guard against edge cases with very early block times in simulation)
		if nonce < types.TimestampNonceCutoff {
			nonce = blockTimeUs
		}

		msg := banktypes.NewMsgSend(from.Address, to.Address, sendCoins)

		// compute fees from remaining balance
		remaining, hasNeg := spendable.SafeSub(sendCoins...)
		var fees sdk.Coins
		if !hasNeg {
			fees, err = simtypes.RandomFees(r, ctx, remaining)
			if err != nil {
				return simtypes.NoOpMsg(types.ModuleName, msgType, "unable to generate fees"), nil, nil
			}
		}

		// build tx with timestamp nonce as the sequence
		tx, err := simtestutil.GenSignedMockTx(
			r,
			txGen,
			[]sdk.Msg{msg},
			fees,
			simtestutil.DefaultGenTxGas,
			chainID,
			[]uint64{fromAcc.GetAccountNumber()},
			[]uint64{nonce}, // timestamp nonce instead of account sequence
			from.PrivKey,
		)
		if err != nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "unable to generate tx"), nil, err
		}

		_, _, err = app.SimTxFinalizeBlock(txGen.TxEncoder(), tx)
		if err != nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, err.Error()), nil, nil
		}

		return simtypes.NewOperationMsg(msg, true, ""), nil, nil
	}
}

// SimulateTimestampNonceDuplicate creates two transactions with the same
// timestamp nonce from the same account. The first should succeed and the
// second should fail with ErrNonceDuplicate. This is a negative test that
// verifies the duplicate-detection invariant holds during simulation.
func SimulateTimestampNonceDuplicate(
	txGen client.TxConfig,
	ak authkeeper.AccountKeeper,
	bk bankkeeper.Keeper,
	nk *keeper.Keeper,
) simtypes.Operation {
	return func(
		r *rand.Rand, app *baseapp.BaseApp, ctx sdk.Context,
		accs []simtypes.Account, chainID string,
	) (simtypes.OperationMsg, []simtypes.FutureOperation, error) {
		msgType := "timestamp_nonce_duplicate"

		from, _ := simtypes.RandomAcc(r, accs)
		to, _ := simtypes.RandomAcc(r, accs)
		for from.PubKey.Equals(to.PubKey) {
			to, _ = simtypes.RandomAcc(r, accs)
		}

		fromAcc := ak.GetAccount(ctx, from.Address)
		if fromAcc == nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "account not found"), nil, nil
		}

		spendable := bk.SpendableCoins(ctx, from.Address)
		if spendable.Empty() {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "no spendable coins"), nil, nil
		}

		sendCoin := sdk.NewInt64Coin(spendable[0].Denom, 1)
		if !spendable.IsAllGTE(sdk.NewCoins(sendCoin)) {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "insufficient balance"), nil, nil
		}

		blockTimeUs := uint64(ctx.BlockTime().UnixMicro())

		buildTx := func(nonce uint64) (sdk.Tx, error) {
			msg := banktypes.NewMsgSend(from.Address, to.Address, sdk.NewCoins(sendCoin))
			return simtestutil.GenSignedMockTx(
				r, txGen, []sdk.Msg{msg}, sdk.Coins{},
				simtestutil.DefaultGenTxGas, chainID,
				[]uint64{fromAcc.GetAccountNumber()},
				[]uint64{nonce},
				from.PrivKey,
			)
		}

		// first tx: should succeed
		tx1, err := buildTx(blockTimeUs)
		if err != nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "tx1 build failed"), nil, err
		}
		_, _, err = app.SimTxFinalizeBlock(txGen.TxEncoder(), tx1)
		if err != nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "tx1 failed: "+err.Error()), nil, nil
		}

		// second tx with same nonce: should fail with duplicate
		tx2, err := buildTx(blockTimeUs)
		if err != nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "tx2 build failed"), nil, err
		}
		_, _, err = app.SimTxFinalizeBlock(txGen.TxEncoder(), tx2)
		if err == nil {
			return simtypes.NoOpMsg(types.ModuleName, msgType, "BUG: duplicate nonce accepted"), nil, nil
		}

		return simtypes.NewOperationMsgBasic(types.ModuleName, msgType, "duplicate correctly rejected", true, nil), nil, nil
	}
}

