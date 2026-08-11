package wallet

import "github.com/arkade-os/arkd/pkg/client-lib/types"

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
	SpendableRedeemAmount uint64 `json:"spendable_redeem_amount"`
	// LockedAmount concatenates every still-timelocked onchain balance:
	// boarding UTXOs younger than the unilateral exit delay AND
	// unilateral-exit redemption UTXOs still inside their CSV. The two
	// are indistinguishable here, which is what the split fields below
	// exist to fix; it is kept as-is for callers that want the total.
	LockedAmount []LockedOnchainBalance `json:"locked_amount,omitempty"`
	// LockedRedeemAmount is the redemption-only portion of LockedAmount:
	// unilateral-exit UTXOs still inside their CSV, which mature into
	// SpendableRedeemAmount and become sweepable by CompleteUnroll.
	//
	// A caller driving an exit state machine needs this rather than
	// LockedAmount. A boarding UTXO in the locked set means "someone just
	// deposited", not "this exit has another tranche coming", and it
	// matures into the boarding-spendable bucket — never into
	// SpendableRedeemAmount. An exit that mistakes one for the other
	// waits for a maturation that never arrives on the bucket it watches.
	LockedRedeemAmount []LockedOnchainBalance `json:"locked_redeem_amount,omitempty"`
	// LockedBoardingAmount is the boarding-only portion of LockedAmount:
	// confirmed boarding UTXOs younger than the unilateral exit delay,
	// which mature into the boarding-spendable bucket.
	LockedBoardingAmount []LockedOnchainBalance `json:"locked_boarding_amount,omitempty"`
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

type getVtxosFilter struct {
	// If true, will sort coins by expiration (oldest first)
	withoutExpirySorting bool
	// If specified, will select only coins in the list
	outpoints []types.Outpoint
	// If true, will select recoverable (swept but unspent) vtxos first
	withRecoverableVtxos bool
	// If specified, will select only vtxos below the given expiration threshold (seconds)
	expiryThreshold int64
	// If true, will recompute the expiration of all vtxos from their anchestor batch outputs
	recomputeExpiry bool
	// If set, the provided vtxo set is used and won't be fetched from network
	vtxos []types.VtxoWithTapTree
	// If set, the provided boarding utxo set is used and won't be fetched from network
	utxos []types.Utxo
	// If true, coin selection will exclude vtxos holding assets
	excludeAssetVtxos bool
}
