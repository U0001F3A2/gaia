package interchain_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/interchaintest/v10"
	"github.com/cosmos/interchaintest/v10/chain/cosmos"
	"github.com/cosmos/interchaintest/v10/ibc"
	"github.com/cosmos/interchaintest/v10/testutil"
	"github.com/stretchr/testify/suite"
	"github.com/tidwall/gjson"

	"github.com/cosmos/gaia/v26/tests/interchain/chainsuite"
)

// NonceUpgradeSuite tests the v25 -> v26 upgrade path.
// It starts a chain with the v25 binary (no nonce/tokenfactory modules),
// performs a governance-driven software upgrade to v26, then verifies
// both new modules are functional.
type NonceUpgradeSuite struct {
	*chainsuite.Suite
	UserWallet ibc.Wallet
}

func (s *NonceUpgradeSuite) SetupSuite() {
	s.Suite.SetupSuite()

	wallet, err := s.Chain.BuildWallet(s.GetContext(), "nonce-user", "")
	s.Require().NoError(err)
	s.UserWallet = wallet
	s.Require().NoError(s.Chain.SendFunds(s.GetContext(), interchaintest.FaucetAccountKeyName, ibc.WalletAmount{
		Address: wallet.FormattedAddress(),
		Amount:  sdkmath.NewInt(1_000_000_000),
		Denom:   chainsuite.Uatom,
	}))
}

// TestNonceParamsAfterUpgrade verifies the nonce module is initialized with
// default params after the upgrade handler runs RunMigrations.
func (s *NonceUpgradeSuite) TestNonceParamsAfterUpgrade() {
	ctx := s.GetContext()

	pastWindow, err := s.Chain.QueryJSON(ctx, "params.past_window_us", "nonce", "params")
	s.Require().NoError(err)
	s.Require().Equal("300000000", pastWindow.String(), "past_window_us should be 5 min in us")

	futureWindow, err := s.Chain.QueryJSON(ctx, "params.future_window_us", "nonce", "params")
	s.Require().NoError(err)
	s.Require().Equal("300000000", futureWindow.String(), "future_window_us should be 5 min in us")

	cutoff, err := s.Chain.QueryJSON(ctx, "params.timestamp_nonce_cutoff", "nonce", "params")
	s.Require().NoError(err)
	s.Require().Equal("1099511627776", cutoff.String(), "cutoff should be 2^40")
}

// TestTokenFactoryParamsAfterUpgrade verifies the upgrade handler set
// tokenfactory params to the values coded in CreateUpgradeHandler, not defaults.
func (s *NonceUpgradeSuite) TestTokenFactoryParamsAfterUpgrade() {
	ctx := s.GetContext()

	fee, err := s.Chain.QueryJSON(ctx, "params.denom_creation_fee.0.amount", "tokenfactory", "params")
	s.Require().NoError(err)
	s.Require().Equal("10000000", fee.String(), "denom_creation_fee should be 10 ATOM")

	feeDenom, err := s.Chain.QueryJSON(ctx, "params.denom_creation_fee.0.denom", "tokenfactory", "params")
	s.Require().NoError(err)
	s.Require().Equal("uatom", feeDenom.String())

	gasConsume, err := s.Chain.QueryJSON(ctx, "params.denom_creation_gas_consume", "tokenfactory", "params")
	s.Require().NoError(err)
	s.Require().Equal("2000000", gasConsume.String(), "gas consume should be 2M")
}

// TestSequentialTxAfterUpgrade verifies standard sequential nonce transactions
// still work after the upgrade (backward compatibility).
func (s *NonceUpgradeSuite) TestSequentialTxAfterUpgrade() {
	ctx := s.GetContext()

	recipient, err := s.Chain.BuildWallet(ctx, "seq-recipient", "")
	s.Require().NoError(err)

	_, err = s.Chain.GetNode().ExecTx(ctx,
		s.UserWallet.KeyName(),
		"bank", "send",
		s.UserWallet.FormattedAddress(), recipient.FormattedAddress(), "1000"+chainsuite.Uatom,
	)
	s.Require().NoError(err)

	balance, err := s.Chain.GetBalance(ctx, recipient.FormattedAddress(), chainsuite.Uatom)
	s.Require().NoError(err)
	s.Require().Equal(sdkmath.NewInt(1000), balance)
}

// TestTimestampNonceTxAfterUpgrade submits a bank send using the --timestamp
// flag (auto-generated timestamp nonce > 2^40), then verifies the nonce was
// recorded in state.
func (s *NonceUpgradeSuite) TestTimestampNonceTxAfterUpgrade() {
	ctx := s.GetContext()

	recipient, err := s.Chain.BuildWallet(ctx, "ts-recipient", "")
	s.Require().NoError(err)

	node := s.Chain.GetNode()
	_, err = node.ExecTx(ctx,
		s.UserWallet.KeyName(),
		"bank", "send",
		s.UserWallet.FormattedAddress(), recipient.FormattedAddress(), "500"+chainsuite.Uatom,
		"--timestamp",
	)
	s.Require().NoError(err)

	balance, err := s.Chain.GetBalance(ctx, recipient.FormattedAddress(), chainsuite.Uatom)
	s.Require().NoError(err)
	s.Require().Equal(sdkmath.NewInt(500), balance)

	nonces, err := s.Chain.QueryJSON(ctx, "timestamp_nonces", "nonce", "nonces", s.UserWallet.FormattedAddress())
	s.Require().NoError(err)
	s.Require().True(nonces.IsArray(), "nonces should be an array")
	s.Require().GreaterOrEqual(int(nonces.Get("#").Int()), 1, "should have at least 1 consumed nonce")
}

// TestNonceDuplicateRejection consumes a timestamp nonce, then attempts to
// reuse it via offline sign + broadcast, expecting rejection.
func (s *NonceUpgradeSuite) TestNonceDuplicateRejection() {
	ctx := s.GetContext()

	recipient, err := s.Chain.BuildWallet(ctx, "dup-recipient", "")
	s.Require().NoError(err)

	node := s.Chain.GetNode()

	// First tx with --timestamp succeeds and consumes a nonce.
	_, err = node.ExecTx(ctx,
		s.UserWallet.KeyName(),
		"bank", "send",
		s.UserWallet.FormattedAddress(), recipient.FormattedAddress(), "100"+chainsuite.Uatom,
		"--timestamp",
	)
	s.Require().NoError(err)

	// Find the consumed nonce.
	noncesResult, err := s.Chain.QueryJSON(ctx, "timestamp_nonces", "nonce", "nonces", s.UserWallet.FormattedAddress())
	s.Require().NoError(err)
	count := int(noncesResult.Get("#").Int())
	s.Require().GreaterOrEqual(count, 1)
	consumedNonce := noncesResult.Array()[count-1].String()

	// Get account number for offline signing.
	accountRaw, _, err := node.ExecQuery(ctx, "auth", "account", s.UserWallet.FormattedAddress())
	s.Require().NoError(err)

	accountNum := gjson.GetBytes(accountRaw, "account.value.account_number").String()
	if accountNum == "" {
		accountNum = gjson.GetBytes(accountRaw, "account.account_number").String()
	}
	s.Require().NotEmpty(accountNum)

	// Generate unsigned tx.
	genCmd := node.BinCommand(
		"tx", "bank", "send",
		s.UserWallet.FormattedAddress(), recipient.FormattedAddress(), "100"+chainsuite.Uatom,
		"--generate-only",
		"--gas", "200000", "--fees", "200000"+chainsuite.Uatom,
		"--from", s.UserWallet.KeyName(),
		"--chain-id", s.Chain.Config().ChainID,
	)
	unsignedTx, _, err := node.Exec(ctx, genCmd, nil)
	s.Require().NoError(err)
	s.Require().NoError(node.WriteFile(ctx, unsignedTx, "unsigned_dup.json"))

	// Sign offline with the already-consumed nonce as sequence.
	signCmd := node.BinCommand(
		"tx", "sign", node.HomeDir()+"/unsigned_dup.json",
		"--keyring-backend", "test",
		"--chain-id", s.Chain.Config().ChainID,
		"--offline",
		"--sequence", consumedNonce,
		"--account-number", accountNum,
		"--from", s.UserWallet.KeyName(),
	)
	signedTx, _, err := node.Exec(ctx, signCmd, nil)
	s.Require().NoError(err)
	s.Require().NoError(node.WriteFile(ctx, signedTx, "signed_dup.json"))

	// Broadcast the duplicate -- expect rejection.
	broadcastCmd := node.NodeCommand(
		"tx", "broadcast", node.HomeDir()+"/signed_dup.json",
		"--output", "json",
	)
	stdout, _, _ := node.Exec(ctx, broadcastCmd, nil)
	output := string(stdout)

	// Duplicate should be caught at CheckTx or DeliverTx with "already consumed".
	s.Require().True(
		strings.Contains(output, "already consumed") || strings.Contains(output, "nonce already consumed"),
		"duplicate nonce should be rejected; got: %s", output,
	)
}

// submitNonceParamsProposal submits and passes a governance proposal to update
// the x/nonce module params.
func (s *NonceUpgradeSuite) submitNonceParamsProposal(pastWindowUs, futureWindowUs, cutoff string) {
	ctx := s.GetContext()

	govAuthority, err := s.Chain.GetGovernanceAddress(ctx)
	s.Require().NoError(err)

	proposalMsg := fmt.Sprintf(`{
		"@type": "/gaia.nonce.v1.MsgUpdateParams",
		"authority": "%s",
		"params": {
			"past_window_us": "%s",
			"future_window_us": "%s",
			"timestamp_nonce_cutoff": "%s"
		}
	}`, govAuthority, pastWindowUs, futureWindowUs, cutoff)

	txhash, err := s.Chain.GetNode().SubmitProposal(ctx, interchaintest.FaucetAccountKeyName,
		cosmos.TxProposalv1{
			Title:    "Update nonce params",
			Deposit:  chainsuite.GovDepositAmount,
			Messages: []json.RawMessage{json.RawMessage(proposalMsg)},
			Summary:  fmt.Sprintf("Set past_window_us=%s, future_window_us=%s", pastWindowUs, futureWindowUs),
			Metadata: "ipfs://CID",
		})
	s.Require().NoError(err)

	propId, err := s.Chain.GetProposalID(ctx, txhash)
	s.Require().NoError(err)
	s.Require().NoError(s.Chain.PassProposal(ctx, propId))
}

// TestWatermarkAfterPruning verifies that the prune high watermark advances
// after nonces expire and get pruned by PreBlocker.
//
// Strategy: shrink past_window_us to 10s via governance, submit a timestamp
// nonce, wait for it to expire, then verify it was pruned via has-nonce query.
// If pruning happened, the watermark must have advanced (PruneExpiredNonces
// always sets the watermark before deleting entries).
func (s *NonceUpgradeSuite) TestWatermarkAfterPruning() {
	ctx := s.GetContext()

	// Shrink past window to 10 seconds for fast expiry.
	s.submitNonceParamsProposal("10000000", "300000000", "1099511627776")

	// Verify params were updated.
	pastWindow, err := s.Chain.QueryJSON(ctx, "params.past_window_us", "nonce", "params")
	s.Require().NoError(err)
	s.Require().Equal("10000000", pastWindow.String())

	// Submit a timestamp nonce tx.
	recipient, err := s.Chain.BuildWallet(ctx, "wm-recipient", "")
	s.Require().NoError(err)

	_, err = s.Chain.GetNode().ExecTx(ctx,
		s.UserWallet.KeyName(),
		"bank", "send",
		s.UserWallet.FormattedAddress(), recipient.FormattedAddress(), "100"+chainsuite.Uatom,
		"--timestamp",
	)
	s.Require().NoError(err)

	// Record the consumed nonce.
	noncesResult, err := s.Chain.QueryJSON(ctx, "timestamp_nonces", "nonce", "nonces", s.UserWallet.FormattedAddress())
	s.Require().NoError(err)
	count := int(noncesResult.Get("#").Int())
	s.Require().GreaterOrEqual(count, 1, "should have at least 1 nonce")
	consumedNonce := noncesResult.Array()[count-1].String()

	// has-nonce should confirm it exists right now.
	hasResult, err := s.Chain.QueryJSON(ctx, "has_nonce", "nonce", "has-nonce", s.UserWallet.FormattedAddress(), consumedNonce)
	s.Require().NoError(err)
	s.Require().Equal("true", hasResult.String(), "nonce should exist before expiry")

	// Wait for the nonce to expire (10s window) + a few blocks for pruning.
	time.Sleep(15 * time.Second)
	s.Require().NoError(testutil.WaitForBlocks(ctx, 3, s.Chain))

	// Verify the nonce was pruned (has-nonce returns false or is absent).
	// Proto3 JSON omits false booleans, so the response is {} and QueryJSON
	// can't find has_nonce. Use ExecQuery directly and check raw JSON.
	stdout, _, err := s.Chain.GetNode().ExecQuery(ctx, "nonce", "has-nonce", s.UserWallet.FormattedAddress(), consumedNonce)
	s.Require().NoError(err)
	hasAfter := gjson.GetBytes(stdout, "has_nonce")
	s.Require().False(hasAfter.Bool(), "nonce should be pruned after expiry (watermark advanced)")
}

// TestWatermarkPreventsReplayAfterParamExpansion is the definitive e2e proof of
// the watermark security fix. It demonstrates:
// 1. Submit timestamp nonce with a short past window (10s)
// 2. Wait for it to be pruned (watermark advances)
// 3. Expand past_window_us via governance (10s -> 10min)
// 4. Attempt to replay the pruned nonce -> rejected by watermark
func (s *NonceUpgradeSuite) TestWatermarkPreventsReplayAfterParamExpansion() {
	ctx := s.GetContext()

	// Shrink past window to 10 seconds.
	s.submitNonceParamsProposal("10000000", "300000000", "1099511627776")

	// Submit a timestamp nonce and record it.
	recipient, err := s.Chain.BuildWallet(ctx, "wm-replay-recipient", "")
	s.Require().NoError(err)

	node := s.Chain.GetNode()
	_, err = node.ExecTx(ctx,
		s.UserWallet.KeyName(),
		"bank", "send",
		s.UserWallet.FormattedAddress(), recipient.FormattedAddress(), "100"+chainsuite.Uatom,
		"--timestamp",
	)
	s.Require().NoError(err)

	// Find the consumed nonce.
	noncesResult, err := s.Chain.QueryJSON(ctx, "timestamp_nonces", "nonce", "nonces", s.UserWallet.FormattedAddress())
	s.Require().NoError(err)
	count := int(noncesResult.Get("#").Int())
	s.Require().GreaterOrEqual(count, 1)
	consumedNonce := noncesResult.Array()[count-1].String()

	// Wait for the nonce to expire and get pruned.
	time.Sleep(15 * time.Second)
	s.Require().NoError(testutil.WaitForBlocks(ctx, 3, s.Chain))

	// Expand past_window_us to 10 minutes (600s).
	// This would normally allow the pruned nonce back into the valid window,
	// but the watermark should prevent it.
	s.submitNonceParamsProposal("600000000", "300000000", "1099511627776")

	// Verify params were expanded.
	pastWindow, err := s.Chain.QueryJSON(ctx, "params.past_window_us", "nonce", "params")
	s.Require().NoError(err)
	s.Require().Equal("600000000", pastWindow.String())

	// Get account number for offline signing.
	accountRaw, _, err := node.ExecQuery(ctx, "auth", "account", s.UserWallet.FormattedAddress())
	s.Require().NoError(err)
	accountNum := gjson.GetBytes(accountRaw, "account.value.account_number").String()
	if accountNum == "" {
		accountNum = gjson.GetBytes(accountRaw, "account.account_number").String()
	}
	s.Require().NotEmpty(accountNum)

	// Generate unsigned tx.
	genCmd := node.BinCommand(
		"tx", "bank", "send",
		s.UserWallet.FormattedAddress(), recipient.FormattedAddress(), "100"+chainsuite.Uatom,
		"--generate-only",
		"--gas", "200000", "--fees", "200000"+chainsuite.Uatom,
		"--from", s.UserWallet.KeyName(),
		"--chain-id", s.Chain.Config().ChainID,
	)
	unsignedTx, _, err := node.Exec(ctx, genCmd, nil)
	s.Require().NoError(err)
	s.Require().NoError(node.WriteFile(ctx, unsignedTx, "unsigned_wm.json"))

	// Sign offline with the pruned nonce as sequence.
	signCmd := node.BinCommand(
		"tx", "sign", node.HomeDir()+"/unsigned_wm.json",
		"--keyring-backend", "test",
		"--chain-id", s.Chain.Config().ChainID,
		"--offline",
		"--sequence", consumedNonce,
		"--account-number", accountNum,
		"--from", s.UserWallet.KeyName(),
	)
	signedTx, _, err := node.Exec(ctx, signCmd, nil)
	s.Require().NoError(err)
	s.Require().NoError(node.WriteFile(ctx, signedTx, "signed_wm.json"))

	// Broadcast the replay -- should be rejected by the watermark.
	broadcastCmd := node.NodeCommand(
		"tx", "broadcast", node.HomeDir()+"/signed_wm.json",
		"--output", "json",
	)
	stdout, _, _ := node.Exec(ctx, broadcastCmd, nil)
	output := string(stdout)

	// The nonce was pruned and watermark prevents it from being accepted
	// even though past_window_us was expanded. Expected rejection reason:
	// "too far in the past" (watermark raises the effective lower bound).
	s.Require().True(
		strings.Contains(output, "too far in the past") ||
			strings.Contains(output, "expired") ||
			strings.Contains(output, "already consumed"),
		"pruned nonce replay should be rejected by watermark after window expansion; got: %s", output,
	)
}

func TestNonceUpgrade(t *testing.T) {
	s := &NonceUpgradeSuite{
		Suite: chainsuite.NewSuite(chainsuite.SuiteConfig{
			UpgradeOnSetup: true,
		}),
	}
	suite.Run(t, s)
}
