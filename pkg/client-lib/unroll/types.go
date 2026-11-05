package unroll

import (
	"errors"
	"fmt"
)

// UnrollRes is the result of a single Unroll iteration (one parent + child 1C1P package).
type UnrollRes struct {
	ParentTx   string
	ParentTxid string
	ChildTx    string
	ChildTxid  string
}

var ErrWaitingForConfirmation = fmt.Errorf("waiting for confirmation(s), please retry later")

// ErrInsufficientOnchainFunds is returned when the wallet has no (or not
// enough) spendable onchain funds to pay the fees required to bump a
// unilateral exit's tree transactions. Bumping the anchor outputs is a
// hard requirement for a unilateral exit, so callers should surface this
// clearly rather than treat it as a transient error to retry. A future
// fee-sponsoring flow (paying the exit fees from another account) can key
// off this sentinel with errors.Is.
var ErrInsufficientOnchainFunds = errors.New("insufficient onchain funds to pay unilateral exit fees")
