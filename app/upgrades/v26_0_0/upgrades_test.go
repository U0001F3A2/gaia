package v26_0_0_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	tokenfactorytypes "github.com/cosmos/tokenfactory/x/tokenfactory/types"

	gaiahelpers "github.com/cosmos/gaia/v26/app/helpers"
	v260 "github.com/cosmos/gaia/v26/app/upgrades/v26_0_0"
	noncetypes "github.com/cosmos/gaia/v26/x/nonce/types"
)

// TestUpgradeStoreKeysIncludeNonce verifies that the nonce store key is listed
// in StoreUpgrades.Added so the multistore creates the sub-store on upgrade.
func TestUpgradeStoreKeysIncludeNonce(t *testing.T) {
	added := v260.Upgrade.StoreUpgrades.Added
	require.True(t, slices.Contains(added, noncetypes.StoreKey),
		"StoreUpgrades.Added must contain %q, got %v", noncetypes.StoreKey, added)
}

// TestStoreUpgradesCompleteness is a regression guard that both tokenfactory
// and nonce are present in Added. A future refactor that removes either would
// break the upgrade.
func TestStoreUpgradesCompleteness(t *testing.T) {
	added := v260.Upgrade.StoreUpgrades.Added
	require.True(t, slices.Contains(added, tokenfactorytypes.ModuleName),
		"StoreUpgrades.Added must contain %q", tokenfactorytypes.ModuleName)
	require.True(t, slices.Contains(added, noncetypes.ModuleName),
		"StoreUpgrades.Added must contain %q", noncetypes.ModuleName)
}

// TestNonceStoreKeyRegistered boots the full app and verifies the nonce
// KVStoreKey is mounted in the multistore.
func TestNonceStoreKeyRegistered(t *testing.T) {
	app := gaiahelpers.Setup(t)
	key := app.GetKey(noncetypes.StoreKey)
	require.NotNil(t, key, "app must have a KVStoreKey for %q", noncetypes.StoreKey)
}

// TestNonceParamsInitialized verifies that after InitGenesis the nonce params
// are set to the module defaults.
func TestNonceParamsInitialized(t *testing.T) {
	app := gaiahelpers.Setup(t)
	ctx := app.NewUncachedContext(true, tmproto.Header{})

	params, err := app.NonceKeeper.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, noncetypes.DefaultParams(), params)
}

// TestNonceKeeperFunctional exercises the full write/read/duplicate-detection
// path through the keeper after app init.
func TestNonceKeeperFunctional(t *testing.T) {
	app := gaiahelpers.Setup(t)

	now := time.Now()
	ctx := app.NewUncachedContext(true, tmproto.Header{
		Time: now,
	})

	// Initialize watermark as PreBlocker would in real block processing.
	err := app.NonceKeeper.PruneExpiredNonces(ctx)
	require.NoError(t, err)

	priv := secp256k1.GenPrivKey()
	addr := sdk.AccAddress(priv.PubKey().Address())
	tsUs := uint64(now.UnixMicro())

	// SetNonce + HasNonce round-trip
	err = app.NonceKeeper.SetNonce(ctx, addr, tsUs)
	require.NoError(t, err)

	has, err := app.NonceKeeper.HasNonce(ctx, addr, tsUs)
	require.NoError(t, err)
	require.True(t, has, "nonce should exist after SetNonce")

	// ValidateAndConsumeTimestampNonce with a fresh nonce succeeds
	tsUs2 := tsUs + 1
	err = app.NonceKeeper.ValidateAndConsumeTimestampNonce(ctx, addr, tsUs2)
	require.NoError(t, err)

	// Same nonce again must return ErrNonceDuplicate
	err = app.NonceKeeper.ValidateAndConsumeTimestampNonce(ctx, addr, tsUs2)
	require.ErrorIs(t, err, noncetypes.ErrNonceDuplicate)
}

// TestNonceModuleInVersionMap verifies that the nonce module's
// ConsensusVersion is persisted by InitChainer via SetModuleVersionMap.
func TestNonceModuleInVersionMap(t *testing.T) {
	app := gaiahelpers.Setup(t)
	ctx := app.NewUncachedContext(true, tmproto.Header{})

	vm, err := app.UpgradeKeeper.GetModuleVersionMap(ctx)
	require.NoError(t, err)

	nonceVersion, ok := vm[noncetypes.ModuleName]
	require.True(t, ok, "version map must contain %q", noncetypes.ModuleName)
	require.Equal(t, uint64(1), nonceVersion)
}

// TestNonceGenesisExportAfterInit verifies that ExportGenesis succeeds on a
// freshly initialized chain and returns sensible defaults.
func TestNonceGenesisExportAfterInit(t *testing.T) {
	app := gaiahelpers.Setup(t)
	ctx := app.NewUncachedContext(true, tmproto.Header{})

	gs := app.NonceKeeper.ExportGenesis(ctx)
	require.NotNil(t, gs)
	require.Equal(t, noncetypes.DefaultParams(), gs.Params)
	require.Empty(t, gs.NonceEntries, "fresh chain should have no nonce entries")
}
