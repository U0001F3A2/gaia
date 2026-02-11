package nonce

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "Params",
					Use:       "params",
					Short:     "Query the x/nonce module parameters",
				},
				{
					RpcMethod: "HasNonce",
					Use:       "has-nonce [address] [timestamp-us]",
					Short:     "Check if a timestamp nonce has been consumed",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "address"},
						{ProtoField: "timestamp_us"},
					},
				},
				{
					RpcMethod: "NoncesByAddress",
					Use:       "nonces [address]",
					Short:     "List all active timestamp nonces for an address",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "address"},
					},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:           types.Msg_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{},
		},
	}
}
