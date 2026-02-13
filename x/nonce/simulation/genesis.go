package simulation

import (
	"encoding/json"
	"fmt"
	"math/rand"

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
		TimestampNonceCutoff: types.DefaultTimestampNonceCutoff,
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

// RandomParams returns random nonce module params for property testing.
func RandomParams(r *rand.Rand) types.Params {
	pastMinutes := r.Intn(10) + 1
	futureMinutes := r.Intn(10) + 1
	return types.Params{
		PastWindowUs:         uint64(pastMinutes) * 60 * 1_000_000,
		FutureWindowUs:       uint64(futureMinutes) * 60 * 1_000_000,
		TimestampNonceCutoff: types.DefaultTimestampNonceCutoff,
	}
}
