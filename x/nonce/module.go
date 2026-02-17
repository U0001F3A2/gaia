package nonce

import (
	"context"
	"encoding/json"
	"fmt"

	gwruntime "github.com/grpc-ecosystem/grpc-gateway/runtime"

	"cosmossdk.io/core/appmodule"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"

	"github.com/cosmos/gaia/v26/x/nonce/keeper"
	noncesim "github.com/cosmos/gaia/v26/x/nonce/simulation"
	"github.com/cosmos/gaia/v26/x/nonce/types"
)

const consensusVersion uint64 = 1

var (
	_ module.AppModuleBasic      = AppModuleBasic{}
	_ module.AppModuleSimulation = AppModule{}
	_ module.HasServices         = AppModule{}
	_ module.HasGenesis          = AppModule{}

	_ appmodule.AppModule     = AppModule{}
	_ appmodule.HasPreBlocker = AppModule{}
)

type AppModuleBasic struct {
	cdc codec.Codec
}

func (AppModuleBasic) Name() string {
	return types.ModuleName
}

func (AppModuleBasic) RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	types.RegisterLegacyAminoCodec(cdc)
}

func (AppModuleBasic) RegisterInterfaces(registry cdctypes.InterfaceRegistry) {
	types.RegisterInterfaces(registry)
}

func (AppModuleBasic) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	return cdc.MustMarshalJSON(types.DefaultGenesisState())
}

func (AppModuleBasic) ValidateGenesis(cdc codec.JSONCodec, _ client.TxEncodingConfig, bz json.RawMessage) error {
	var data types.GenesisState
	if err := cdc.UnmarshalJSON(bz, &data); err != nil {
		return fmt.Errorf("failed to unmarshal %s genesis state: %w", types.ModuleName, err)
	}
	return types.ValidateGenesis(&data)
}

func (AppModuleBasic) RegisterGRPCGatewayRoutes(clientCtx client.Context, mux *gwruntime.ServeMux) {
	if err := types.RegisterQueryHandlerClient(context.Background(), mux, types.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
}

type AppModule struct {
	AppModuleBasic
	keeper *keeper.Keeper
	ak     authkeeper.AccountKeeper
	bk     bankkeeper.Keeper
}

func NewAppModule(cdc codec.Codec, keeper *keeper.Keeper, ak authkeeper.AccountKeeper, bk bankkeeper.Keeper) AppModule {
	return AppModule{
		AppModuleBasic: AppModuleBasic{cdc: cdc},
		keeper:         keeper,
		ak:             ak,
		bk:             bk,
	}
}

func (am AppModule) IsOnePerModuleType() {}
func (am AppModule) IsAppModule()        {}

func (am AppModule) RegisterServices(cfg module.Configurator) {
	types.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(am.keeper))
	types.RegisterQueryServer(cfg.QueryServer(), keeper.Querier{Keeper: am.keeper})
}

func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, data json.RawMessage) {
	var genesisState types.GenesisState
	cdc.MustUnmarshalJSON(data, &genesisState)
	am.keeper.InitGenesis(ctx, &genesisState)
}

func (am AppModule) ExportGenesis(ctx sdk.Context, cdc codec.JSONCodec) json.RawMessage {
	return cdc.MustMarshalJSON(am.keeper.ExportGenesis(ctx))
}

func (AppModule) ConsensusVersion() uint64 { return consensusVersion }

// --- Simulation ---

func (am AppModule) GenerateGenesisState(simState *module.SimulationState) {
	noncesim.RandomizedGenState(simState)
}

func (am AppModule) RegisterStoreDecoder(_ simtypes.StoreDecoderRegistry) {}

func (am AppModule) WeightedOperations(simState module.SimulationState) []simtypes.WeightedOperation {
	return noncesim.WeightedOperations(simState.AppParams, simState.Cdc, simState.TxConfig, am.ak, am.bk, am.keeper)
}

// PreBlock prunes expired timestamp nonces at the start of each block.
// Pruning errors are propagated to halt the chain (fail-stop) rather than
// silently allowing unbounded state growth that could lead to consensus failure.
func (am AppModule) PreBlock(ctx context.Context) (appmodule.ResponsePreBlock, error) {
	if err := am.keeper.PruneExpiredNonces(ctx); err != nil {
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		sdkCtx.Logger().Error("x/nonce: PreBlocker prune failed", "err", err)
		return sdk.ResponsePreBlock{}, fmt.Errorf("failed to prune expired nonces: %w", err)
	}
	return sdk.ResponsePreBlock{}, nil
}
