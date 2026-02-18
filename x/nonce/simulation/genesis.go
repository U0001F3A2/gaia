package simulation

import (
	"encoding/json"
	"fmt"

	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

func RandomizedGenState(simState *module.SimulationState) {
	// randomize past window: 1-10 minutes in microseconds
	pastMinutes := simState.Rand.Intn(10) + 1
	pastWindowUs := uint64(pastMinutes) * 60 * 1_000_000

	// randomize future window: 1-10 minutes in microseconds
	futureMinutes := simState.Rand.Intn(10) + 1
	futureWindowUs := uint64(futureMinutes) * 60 * 1_000_000

	params := types.Params{
		PastWindowUs:         pastWindowUs,
		FutureWindowUs:       futureWindowUs,
		TimestampNonceCutoff: types.TimestampNonceCutoff,
	}

	genesis := types.GenesisState{
		Params: params,
	}

	bz, err := json.MarshalIndent(genesis, "", " ")
	if err != nil {
		panic(err)
	}
	fmt.Printf("Selected randomly generated nonce parameters:\n%s\n", bz)

	simState.GenState[types.ModuleName] = simState.Cdc.MustMarshalJSON(&genesis)
}

