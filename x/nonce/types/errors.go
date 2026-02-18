package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrNonceExpired        = errorsmod.Register(ModuleName, 2, "timestamp nonce is too far in the past")
	ErrNonceTooFarInFuture = errorsmod.Register(ModuleName, 3, "timestamp nonce is too far in the future")
	ErrNonceDuplicate      = errorsmod.Register(ModuleName, 4, "timestamp nonce already consumed")
	ErrNonceOverflow       = errorsmod.Register(ModuleName, 5, "future window overflow")
	ErrCorruptedState      = errorsmod.Register(ModuleName, 6, "corrupted module state")
)
