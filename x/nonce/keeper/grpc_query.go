package keeper

import (
	"bytes"
	"context"

	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/gaia/v26/x/nonce/types"
)

var _ types.QueryServer = Querier{}

// Querier implements the x/nonce gRPC query server.
type Querier struct {
	Keeper *Keeper
}

func (q Querier) Params(ctx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	params, err := q.Keeper.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	return &types.QueryParamsResponse{Params: params}, nil
}

func (q Querier) HasNonce(ctx context.Context, req *types.QueryHasNonceRequest) (*types.QueryHasNonceResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, err
	}
	has, err := q.Keeper.HasNonce(ctx, addr, req.TimestampUs)
	if err != nil {
		return nil, err
	}
	return &types.QueryHasNonceResponse{HasNonce: has}, nil
}

func (q Querier) NoncesByAddress(ctx context.Context, req *types.QueryNoncesByAddressRequest) (*types.QueryNoncesByAddressResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, err
	}

	store := q.Keeper.storeService.OpenKVStore(ctx)
	prefix := types.NonceIteratorPrefix()
	addrBytes := []byte(addr)

	// Determine page limit (default 100, cap at 1000).
	limit := uint64(100)
	if req.Pagination != nil && req.Pagination.Limit > 0 {
		limit = req.Pagination.Limit
	}
	if limit > 1000 {
		limit = 1000
	}

	// Resume from NextKey if provided, otherwise start from prefix.
	start := prefix
	if req.Pagination != nil && len(req.Pagination.Key) > 0 {
		start = req.Pagination.Key
	}
	end := types.NoncePrefixEnd()

	iter, err := store.Iterator(start, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	// Cap total keys scanned to prevent DoS (address is key suffix, so
	// filtering requires a full scan across all timestamps).
	const maxScan = 50_000
	var nonces []uint64
	var scanned uint64
	var nextKey []byte
	for ; iter.Valid() && scanned < maxScan; iter.Next() {
		key := iter.Key()
		if len(key) < types.NonceKeyMinLen {
			continue
		}
		scanned++
		tsUs, keyAddr := types.ParseNonceKey(key)
		if !bytes.Equal(keyAddr, addrBytes) {
			continue
		}
		if uint64(len(nonces)) >= limit {
			return &types.QueryNoncesByAddressResponse{
				TimestampNonces: nonces,
				Pagination: &query.PageResponse{
					NextKey: key,
				},
			}, nil
		}
		nonces = append(nonces, tsUs)
	}

	// If scan budget exhausted while iterator still has keys, set NextKey
	// so the client knows results may be incomplete and can resume.
	if scanned >= maxScan && iter.Valid() {
		nextKey = iter.Key()
	}

	resp := &types.QueryNoncesByAddressResponse{
		TimestampNonces: nonces,
		Pagination:      &query.PageResponse{Total: uint64(len(nonces))},
	}
	if nextKey != nil {
		resp.Pagination.NextKey = nextKey
	}
	return resp, nil
}
