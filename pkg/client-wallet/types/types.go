package types

import (
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
)

const (
	InMemoryStore = "inmemory"
	FileStore     = "file"
)

type FeeInfo clientlib.FeeInfo

type Balance struct {
	OnchainBalance  OnchainBalance    `json:"onchain_balance"`
	OffchainBalance OffchainBalance   `json:"offchain_balance"`
	AssetBalances   map[string]uint64 `json:"asset_balances,omitempty"`
}

type OnchainBalance struct {
	SpendableAmount uint64 `json:"spendable_amount"`
	// SpendableRedeemAmount is the portion of SpendableAmount contributed
	// by matured unilateral-exit redemption UTXOs — i.e. exactly the funds
	// CompleteUnroll can sweep. It excludes ordinary onchain funds (e.g. an
	// anchor-fee reserve) and boarding UTXOs, so callers can tell "the exit
	// is ready to sweep" apart from "the wallet merely holds some spendable
	// onchain balance".
	SpendableRedeemAmount uint64                 `json:"spendable_redeem_amount"`
	LockedAmount          []LockedOnchainBalance `json:"locked_amount,omitempty"`
}

type LockedOnchainBalance struct {
	SpendableAt string `json:"spendable_at"`
	Amount      uint64 `json:"amount"`
}

type OffchainBalance struct {
	Total          uint64        `json:"total"`
	NextExpiration string        `json:"next_expiration,omitempty"`
	Details        []VtxoDetails `json:"details"`
}

type VtxoDetails struct {
	ExpiryTime string `json:"expiry_time"`
	Amount     uint64 `json:"amount"`
}
