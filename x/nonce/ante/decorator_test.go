package ante_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "cosmossdk.io/api/cosmos/crypto/secp256k1"
	"cosmossdk.io/log"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authcodec "github.com/cosmos/cosmos-sdk/x/auth/codec"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	xauthsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/bank"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	nonceante "github.com/cosmos/gaia/v26/x/nonce/ante"
	noncekeeper "github.com/cosmos/gaia/v26/x/nonce/keeper"
	noncetypes "github.com/cosmos/gaia/v26/x/nonce/types"
)

type decoratorTestSuite struct {
	ctx           sdk.Context
	accountKeeper authkeeper.AccountKeeper
	nonceKeeper   *noncekeeper.Keeper
	encCfg        moduletestutil.TestEncodingConfig
	clientCtx     client.Context
	txBuilder     client.TxBuilder
}

func setupDecoratorTest(t *testing.T) *decoratorTestSuite {
	t.Helper()
	s := &decoratorTestSuite{}

	authKey := storetypes.NewKVStoreKey(types.StoreKey)
	nonceKey := storetypes.NewKVStoreKey(noncetypes.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")

	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(authKey, storetypes.StoreTypeIAVL, db)
	cms.MountStoreWithDB(nonceKey, storetypes.StoreTypeIAVL, db)
	cms.MountStoreWithDB(tkey, storetypes.StoreTypeTransient, db)
	require.NoError(t, cms.LoadLatestVersion())

	s.ctx = sdk.NewContext(cms, cmtproto.Header{Time: time.Unix(1738780800, 0), ChainID: "test-chain"}, false, log.NewNopLogger()).
		WithBlockHeight(1)

	s.encCfg = moduletestutil.MakeTestEncodingConfig(auth.AppModuleBasic{}, bank.AppModuleBasic{})
	testdata.RegisterInterfaces(s.encCfg.InterfaceRegistry)

	maccPerms := map[string][]string{
		"fee_collector": nil,
	}
	s.accountKeeper = authkeeper.NewAccountKeeper(
		s.encCfg.Codec, runtime.NewKVStoreService(authKey),
		types.ProtoBaseAccount, maccPerms, authcodec.NewBech32Codec("cosmos"),
		sdk.Bech32MainPrefix, types.NewModuleAddress("gov").String(),
	)
	require.NoError(t, s.accountKeeper.Params.Set(s.ctx, types.DefaultParams()))

	cdc := codec.NewProtoCodec(codectypes.NewInterfaceRegistry())
	nonceStoreService := runtime.NewKVStoreService(nonceKey)
	nk := noncekeeper.NewKeeper(cdc, nonceStoreService, "cosmos1authority")
	require.NoError(t, nk.SetParams(s.ctx, noncetypes.DefaultParams()))
	s.nonceKeeper = nk

	s.clientCtx = client.Context{}.WithTxConfig(s.encCfg.TxConfig)
	s.txBuilder = s.clientCtx.TxConfig.NewTxBuilder()

	return s
}

type testAccount struct {
	acc  sdk.AccountI
	priv cryptotypes.PrivKey
}

func (s *decoratorTestSuite) sigVerifyHandler() sdk.AnteHandler {
	svd := nonceante.NewSigVerificationDecorator(
		s.accountKeeper,
		s.encCfg.TxConfig.SignModeHandler(),
		s.nonceKeeper,
	)
	return sdk.ChainAnteDecorators(svd)
}

func (s *decoratorTestSuite) fullChainHandler() sdk.AnteHandler {
	svd := nonceante.NewSigVerificationDecorator(
		s.accountKeeper,
		s.encCfg.TxConfig.SignModeHandler(),
		s.nonceKeeper,
	)
	isd := nonceante.NewIncrementSequenceDecorator(s.accountKeeper)
	return sdk.ChainAnteDecorators(svd, isd)
}

func (s *decoratorTestSuite) blockTimeUs() uint64 {
	return uint64(s.ctx.BlockTime().UnixMicro())
}

func (s *decoratorTestSuite) createAccount(t *testing.T, accNum uint64) testAccount {
	t.Helper()
	priv, pub, addr := testdata.KeyTestPubAddr()
	acc := s.accountKeeper.NewAccountWithAddress(s.ctx, addr)
	require.NoError(t, acc.SetAccountNumber(accNum))
	require.NoError(t, acc.SetPubKey(pub))
	s.accountKeeper.SetAccount(s.ctx, acc)
	return testAccount{acc: acc, priv: priv}
}

func (s *decoratorTestSuite) createSignedTx(
	t *testing.T, privs []cryptotypes.PrivKey,
	accNums, accSeqs []uint64, chainID string,
) xauthsigning.Tx {
	t.Helper()

	// Create msg with all signers so len(signers) == len(sigs)
	addrs := make([]sdk.AccAddress, len(privs))
	for i, p := range privs {
		addrs[i] = sdk.AccAddress(p.PubKey().Address())
	}
	msgs := []sdk.Msg{testdata.NewTestMsg(addrs...)}
	require.NoError(t, s.txBuilder.SetMsgs(msgs...))
	s.txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin("atom", 150)))
	s.txBuilder.SetGasLimit(200000)

	// Round 1: set empty signatures
	var sigsV2 []signing.SignatureV2
	for i, priv := range privs {
		sigsV2 = append(sigsV2, signing.SignatureV2{
			PubKey:   priv.PubKey(),
			Data:     &signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_DIRECT},
			Sequence: accSeqs[i],
		})
	}
	require.NoError(t, s.txBuilder.SetSignatures(sigsV2...))

	// Round 2: sign
	sigsV2 = nil
	for i, priv := range privs {
		signerData := xauthsigning.SignerData{
			Address:       sdk.AccAddress(priv.PubKey().Address()).String(),
			ChainID:       chainID,
			AccountNumber: accNums[i],
			Sequence:      accSeqs[i],
			PubKey:        priv.PubKey(),
		}
		sigV2, err := tx.SignWithPrivKey(
			s.ctx, signing.SignMode_SIGN_MODE_DIRECT, signerData,
			s.txBuilder, priv, s.clientCtx.TxConfig, accSeqs[i],
		)
		require.NoError(t, err)
		sigsV2 = append(sigsV2, sigV2)
	}
	require.NoError(t, s.txBuilder.SetSignatures(sigsV2...))

	return s.txBuilder.GetTx()
}

func (s *decoratorTestSuite) createSignedTxWithMode(
	t *testing.T, privs []cryptotypes.PrivKey,
	accNums, accSeqs []uint64, chainID string,
	signMode signing.SignMode,
) xauthsigning.Tx {
	t.Helper()

	addrs := make([]sdk.AccAddress, len(privs))
	for i, p := range privs {
		addrs[i] = sdk.AccAddress(p.PubKey().Address())
	}
	msgs := []sdk.Msg{testdata.NewTestMsg(addrs...)}
	require.NoError(t, s.txBuilder.SetMsgs(msgs...))
	s.txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin("atom", 150)))
	s.txBuilder.SetGasLimit(200000)

	var sigsV2 []signing.SignatureV2
	for i, priv := range privs {
		sigsV2 = append(sigsV2, signing.SignatureV2{
			PubKey:   priv.PubKey(),
			Data:     &signing.SingleSignatureData{SignMode: signMode},
			Sequence: accSeqs[i],
		})
	}
	require.NoError(t, s.txBuilder.SetSignatures(sigsV2...))

	sigsV2 = nil
	for i, priv := range privs {
		signerData := xauthsigning.SignerData{
			Address:       sdk.AccAddress(priv.PubKey().Address()).String(),
			ChainID:       chainID,
			AccountNumber: accNums[i],
			Sequence:      accSeqs[i],
			PubKey:        priv.PubKey(),
		}
		sigV2, err := tx.SignWithPrivKey(
			s.ctx, signMode, signerData,
			s.txBuilder, priv, s.clientCtx.TxConfig, accSeqs[i],
		)
		require.NoError(t, err)
		sigsV2 = append(sigsV2, sigV2)
	}
	require.NoError(t, s.txBuilder.SetSignatures(sigsV2...))

	return s.txBuilder.GetTx()
}

// --- SigVerificationDecorator Tests ---

func TestSigVerify_SequentialNonce(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{0}, "test-chain")
	handler := s.sigVerifyHandler()

	ctx := s.ctx.WithIsSigverifyTx(true)
	_, err := handler(ctx, testTx, false)
	require.NoError(t, err)
}

func TestSigVerify_SequentialNonce_WrongSequence(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{5}, "test-chain")
	handler := s.sigVerifyHandler()

	ctx := s.ctx.WithIsSigverifyTx(true)
	_, err := handler(ctx, testTx, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "account sequence mismatch")
}

func TestSigVerify_TimestampNonce(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	blockTimeUs := s.blockTimeUs()

	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{blockTimeUs}, "test-chain")
	handler := s.sigVerifyHandler()

	_, err := handler(s.ctx, testTx, true) // simulate=true
	require.NoError(t, err)

	// Verify nonce was consumed
	addr := sdk.AccAddress(acct.priv.PubKey().Address())
	has, err := s.nonceKeeper.HasNonce(s.ctx, addr, blockTimeUs)
	require.NoError(t, err)
	require.True(t, has, "timestamp nonce should be consumed")
}

func TestSigVerify_TimestampNonce_RealSignature(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	blockTimeUs := s.blockTimeUs()

	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{blockTimeUs}, "test-chain")
	handler := s.sigVerifyHandler()

	ctx := s.ctx.WithIsSigverifyTx(true)
	_, err := handler(ctx, testTx, false)
	require.NoError(t, err)

	// Verify nonce was consumed
	addr := sdk.AccAddress(acct.priv.PubKey().Address())
	has, err := s.nonceKeeper.HasNonce(s.ctx, addr, blockTimeUs)
	require.NoError(t, err)
	require.True(t, has, "timestamp nonce should be consumed")
}

func TestSigVerify_ExactCutoffBoundary(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	cutoff := noncetypes.TimestampNonceCutoff // 1 << 40
	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{cutoff}, "test-chain")
	handler := s.sigVerifyHandler()

	// 2^40 as a timestamp = Jan 2005, well outside the ±5min window around 2025
	_, err := handler(s.ctx, testTx, true)
	require.ErrorIs(t, err, noncetypes.ErrNonceExpired,
		"sequence at exact cutoff (2^40) should route to timestamp path and be rejected as expired")
}

func TestSigVerify_BelowCutoff_Sequential(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	belowCutoff := noncetypes.TimestampNonceCutoff - 1
	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{belowCutoff}, "test-chain")
	handler := s.sigVerifyHandler()

	// Account sequence is 0, tx sequence is 2^40-1: should fail with sequence mismatch (sequential path)
	_, err := handler(s.ctx, testTx, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "account sequence mismatch",
		"sequence just below cutoff should route to sequential path")
}

func TestSigVerify_TimestampNonce_Duplicate(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	blockTimeUs := s.blockTimeUs()
	handler := s.sigVerifyHandler()

	// First tx: succeeds
	tx1 := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{blockTimeUs}, "test-chain")
	_, err := handler(s.ctx, tx1, true)
	require.NoError(t, err)

	// Second tx with same nonce: fails
	s.txBuilder = s.clientCtx.TxConfig.NewTxBuilder()
	tx2 := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{blockTimeUs}, "test-chain")
	_, err = handler(s.ctx, tx2, true)
	require.ErrorIs(t, err, noncetypes.ErrNonceDuplicate)
}

func TestSigVerify_TimestampNonce_Expired(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	expired := s.blockTimeUs() - noncetypes.DefaultPastWindowUs - 1
	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{expired}, "test-chain")
	handler := s.sigVerifyHandler()

	_, err := handler(s.ctx, testTx, true)
	require.ErrorIs(t, err, noncetypes.ErrNonceExpired)
}

// --- Multi-sig timestamp nonce tests ---

func TestSigVerify_MultiSig_SameTimestamp(t *testing.T) {
	s := setupDecoratorTest(t)
	acct1 := s.createAccount(t, 1000)
	acct2 := s.createAccount(t, 1001)

	blockTimeUs := s.blockTimeUs()

	testTx := s.createSignedTx(t,
		[]cryptotypes.PrivKey{acct1.priv, acct2.priv},
		[]uint64{1000, 1001},
		[]uint64{blockTimeUs, blockTimeUs},
		"test-chain",
	)
	handler := s.sigVerifyHandler()

	_, err := handler(s.ctx, testTx, true)
	require.NoError(t, err)
}

func TestSigVerify_MultiSig_DifferentTimestamps(t *testing.T) {
	s := setupDecoratorTest(t)
	acct1 := s.createAccount(t, 1000)
	acct2 := s.createAccount(t, 1001)

	blockTimeUs := s.blockTimeUs()

	testTx := s.createSignedTx(t,
		[]cryptotypes.PrivKey{acct1.priv, acct2.priv},
		[]uint64{1000, 1001},
		[]uint64{blockTimeUs, blockTimeUs + 1},
		"test-chain",
	)
	handler := s.sigVerifyHandler()

	_, err := handler(s.ctx, testTx, true)
	require.NoError(t, err)

	// Both nonces consumed independently
	addr1 := sdk.AccAddress(acct1.priv.PubKey().Address())
	addr2 := sdk.AccAddress(acct2.priv.PubKey().Address())
	has1, err := s.nonceKeeper.HasNonce(s.ctx, addr1, blockTimeUs)
	require.NoError(t, err)
	require.True(t, has1, "acct1 timestamp nonce should be consumed")
	has2, err := s.nonceKeeper.HasNonce(s.ctx, addr2, blockTimeUs+1)
	require.NoError(t, err)
	require.True(t, has2, "acct2 timestamp nonce should be consumed")
}

// --- IncrementSequenceDecorator Tests ---

func TestIncrementSequence_SequentialSigner(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)
	addr := sdk.AccAddress(acct.priv.PubKey().Address())

	// Account sequence starts at 0
	require.Equal(t, uint64(0), s.accountKeeper.GetAccount(s.ctx, addr).GetSequence())

	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{0}, "test-chain")

	isd := nonceante.NewIncrementSequenceDecorator(s.accountKeeper)
	handler := sdk.ChainAnteDecorators(isd)

	_, err := handler(s.ctx, testTx, false)
	require.NoError(t, err)

	// Sequence should be incremented to 1
	require.Equal(t, uint64(1), s.accountKeeper.GetAccount(s.ctx, addr).GetSequence())
}

func TestIncrementSequence_TimestampSignerSkipped(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)
	addr := sdk.AccAddress(acct.priv.PubKey().Address())

	require.Equal(t, uint64(0), s.accountKeeper.GetAccount(s.ctx, addr).GetSequence())

	blockTimeUs := uint64(s.ctx.BlockTime().UnixMicro())
	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{blockTimeUs}, "test-chain")

	// Set the timestamp signer context (as SigVerificationDecorator would)
	ctx := nonceante.WithTimestampSigners(s.ctx, nonceante.TimestampSignerSet{
		string(addr): true,
	})

	isd := nonceante.NewIncrementSequenceDecorator(s.accountKeeper)
	handler := sdk.ChainAnteDecorators(isd)

	_, err := handler(ctx, testTx, false)
	require.NoError(t, err)

	// Sequence should NOT be incremented (still 0)
	require.Equal(t, uint64(0), s.accountKeeper.GetAccount(s.ctx, addr).GetSequence())
}

// --- Full chain: SigVerify + IncrementSequence ---

func TestFullChain_SequentialTx(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)
	addr := sdk.AccAddress(acct.priv.PubKey().Address())

	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{0}, "test-chain")
	handler := s.fullChainHandler()

	ctx := s.ctx.WithIsSigverifyTx(true)
	_, err := handler(ctx, testTx, false)
	require.NoError(t, err)

	require.Equal(t, uint64(1), s.accountKeeper.GetAccount(s.ctx, addr).GetSequence())
}

func TestFullChain_TimestampTx(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)
	addr := sdk.AccAddress(acct.priv.PubKey().Address())

	blockTimeUs := s.blockTimeUs()
	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{blockTimeUs}, "test-chain")
	handler := s.fullChainHandler()

	_, err := handler(s.ctx, testTx, true) // simulate=true
	require.NoError(t, err)

	require.Equal(t, uint64(0), s.accountKeeper.GetAccount(s.ctx, addr).GetSequence())

	has, err := s.nonceKeeper.HasNonce(s.ctx, addr, blockTimeUs)
	require.NoError(t, err)
	require.True(t, has)
}

// --- Mixed multi-sig: one signer sequential, one timestamp ---

func TestSigVerify_MixedMultiSig_SequentialAndTimestamp(t *testing.T) {
	s := setupDecoratorTest(t)
	acct1 := s.createAccount(t, 1000) // sequential signer (seq=0)
	acct2 := s.createAccount(t, 1001) // timestamp signer

	blockTimeUs := s.blockTimeUs()

	testTx := s.createSignedTx(t,
		[]cryptotypes.PrivKey{acct1.priv, acct2.priv},
		[]uint64{1000, 1001},
		[]uint64{0, blockTimeUs},
		"test-chain",
	)
	handler := s.fullChainHandler()

	_, err := handler(s.ctx, testTx, true)
	require.NoError(t, err)

	// acct1 (sequential): sequence incremented
	addr1 := sdk.AccAddress(acct1.priv.PubKey().Address())
	require.Equal(t, uint64(1), s.accountKeeper.GetAccount(s.ctx, addr1).GetSequence(),
		"sequential signer should have incremented sequence")

	// acct2 (timestamp): sequence NOT incremented, nonce consumed
	addr2 := sdk.AccAddress(acct2.priv.PubKey().Address())
	require.Equal(t, uint64(0), s.accountKeeper.GetAccount(s.ctx, addr2).GetSequence(),
		"timestamp signer should not have incremented sequence")
	has, err := s.nonceKeeper.HasNonce(s.ctx, addr2, blockTimeUs)
	require.NoError(t, err)
	require.True(t, has, "timestamp nonce should be consumed for acct2")
}

// --- Edge case: timestamp nonce at exact future boundary via ante handler ---

func TestSigVerify_TimestampNonce_ExactFutureBoundary(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	exactUpper := s.blockTimeUs() + noncetypes.DefaultFutureWindowUs
	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{exactUpper}, "test-chain")
	handler := s.sigVerifyHandler()

	_, err := handler(s.ctx, testTx, true)
	require.NoError(t, err, "nonce at exact future boundary should be accepted")
}

func TestSigVerify_TimestampNonce_PastFutureBoundary(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	pastUpper := s.blockTimeUs() + noncetypes.DefaultFutureWindowUs + 1
	testTx := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{pastUpper}, "test-chain")
	handler := s.sigVerifyHandler()

	_, err := handler(s.ctx, testTx, true)
	require.ErrorIs(t, err, noncetypes.ErrNonceTooFarInFuture, "nonce past future boundary should be rejected")
}

// --- H1: Multi-signer rollback test ---

func TestSigVerify_MultiSig_Signer2Fails_Signer1RolledBack(t *testing.T) {
	s := setupDecoratorTest(t)
	acct1 := s.createAccount(t, 1000)
	acct2 := s.createAccount(t, 1001)

	blockTimeUs := s.blockTimeUs()
	expiredNonce := blockTimeUs - noncetypes.DefaultPastWindowUs - 1

	testTx := s.createSignedTx(t,
		[]cryptotypes.PrivKey{acct1.priv, acct2.priv},
		[]uint64{1000, 1001},
		[]uint64{blockTimeUs, expiredNonce},
		"test-chain",
	)
	handler := s.sigVerifyHandler()

	// Use a CacheMultiStore branch (as the real ante handler does)
	branchedCtx, _ := s.ctx.CacheContext()
	_, err := handler(branchedCtx, testTx, true)
	require.ErrorIs(t, err, noncetypes.ErrNonceExpired,
		"signer2's expired nonce should cause tx failure")

	// Since we did NOT call write() on the branch, signer1's nonce must NOT
	// be consumed in the original (parent) store.
	addr1 := sdk.AccAddress(acct1.priv.PubKey().Address())
	has, err := s.nonceKeeper.HasNonce(s.ctx, addr1, blockTimeUs)
	require.NoError(t, err)
	require.False(t, has,
		"signer1's nonce should be rolled back when signer2 fails (CacheMultiStore discard)")
}

// --- H2: RecheckTx test ---

func TestSigVerify_RecheckTx_SkipsSigVerify_CatchesDuplicate(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	blockTimeUs := s.blockTimeUs()
	handler := s.sigVerifyHandler()

	// First: consume the nonce normally (simulate=true to skip sig verify)
	tx1 := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{blockTimeUs}, "test-chain")
	_, err := handler(s.ctx, tx1, true)
	require.NoError(t, err)

	// RecheckTx: IsReCheckTx=true means sig verification is skipped,
	// but the timestamp nonce duplicate check should still catch it.
	recheckCtx := s.ctx.WithIsReCheckTx(true)
	s.txBuilder = s.clientCtx.TxConfig.NewTxBuilder()
	tx2 := s.createSignedTx(t, []cryptotypes.PrivKey{acct.priv}, []uint64{1000}, []uint64{blockTimeUs}, "test-chain")
	_, err = handler(recheckCtx, tx2, false)
	require.ErrorIs(t, err, noncetypes.ErrNonceDuplicate,
		"RecheckTx should catch duplicate timestamp nonce even without sig verification")
}

// --- H18: Sign mode agnostic tests ---

func TestSigVerify_TimestampNonce_AminoJSON(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	blockTimeUs := s.blockTimeUs()

	testTx := s.createSignedTxWithMode(t,
		[]cryptotypes.PrivKey{acct.priv},
		[]uint64{1000}, []uint64{blockTimeUs},
		"test-chain",
		signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON,
	)
	handler := s.sigVerifyHandler()

	_, err := handler(s.ctx, testTx, true)
	require.NoError(t, err, "timestamp nonce should work with SIGN_MODE_LEGACY_AMINO_JSON")

	addr := sdk.AccAddress(acct.priv.PubKey().Address())
	has, err := s.nonceKeeper.HasNonce(s.ctx, addr, blockTimeUs)
	require.NoError(t, err)
	require.True(t, has, "nonce should be consumed regardless of sign mode")
}

func TestSigVerify_SequentialNonce_AminoJSON(t *testing.T) {
	s := setupDecoratorTest(t)
	acct := s.createAccount(t, 1000)

	testTx := s.createSignedTxWithMode(t,
		[]cryptotypes.PrivKey{acct.priv},
		[]uint64{1000}, []uint64{0},
		"test-chain",
		signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON,
	)
	handler := s.sigVerifyHandler()

	ctx := s.ctx.WithIsSigverifyTx(true)
	_, err := handler(ctx, testTx, false)
	require.NoError(t, err, "sequential nonce should work with SIGN_MODE_LEGACY_AMINO_JSON")
}
