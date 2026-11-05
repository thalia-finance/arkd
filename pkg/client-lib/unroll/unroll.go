package unroll

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/psbt/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lntypes"
	log "github.com/sirupsen/logrus"
)

// UnrollArgs configures Unroll: builds 1C1P bump packages for each given
// vtxo's next branch tx and broadcasts each parent+child pair.
type UnrollArgs struct {
	Explorer     clientlib.Explorer
	Indexer      clientlib.Indexer
	SignTx       clientlib.SignFn
	ServerParams clientlib.ServerParams
	Vtxos        []clientlib.Vtxo
	BumpAddr     string
	BumpPubKey   *btcec.PublicKey
}

func (a UnrollArgs) validate() error {
	if a.Explorer == nil {
		return fmt.Errorf("missing explorer")
	}
	if a.Indexer == nil {
		return fmt.Errorf("missing indexer")
	}
	if a.SignTx == nil {
		return fmt.Errorf("missing sign tx function")
	}
	if a.BumpPubKey == nil {
		return fmt.Errorf("missing bump pub key")
	}
	if len(a.ServerParams.Network.Name) <= 0 {
		return fmt.Errorf("missing server params")
	}
	if len(a.Vtxos) <= 0 {
		return fmt.Errorf("missing vtxos to unroll")
	}
	if len(a.BumpAddr) <= 0 {
		return fmt.Errorf("missing bump address")
	}
	netParams := clientlib.ToBitcoinNetwork(a.ServerParams.Network)
	if _, err := arklib.DecodeBitcoinAddress(a.BumpAddr, &netParams); err != nil {
		return fmt.Errorf("invalid bump address")
	}
	return nil
}

// Unroll iterates over each vtxo's redeem branch, builds a 1C1P bump package
// (parent branch tx + child anchor-bumping tx) and broadcasts the package.
func Unroll(ctx context.Context, args UnrollArgs) ([]UnrollRes, error) {
	if err := args.validate(); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	vtxos := args.Vtxos

	totalVtxosAmount := uint64(0)
	for _, vtxo := range vtxos {
		totalVtxosAmount += vtxo.Amount
	}

	// transactionsMap avoid duplicates
	transactionsMap := make(map[string]struct{}, 0)
	transactions := make([]string, 0)

	branches, err := getBranchesToUnroll(ctx, args.Explorer, args.Indexer, vtxos)
	if err != nil {
		return nil, err
	}

	isWaitingForConfirmation := false

	for _, branch := range branches {
		nextTx, err := branch.NextRedeemTx()
		if err != nil {
			if err, ok := err.(ErrPendingConfirmation); ok {
				// the branch tx is in the mempool, we must wait for confirmation
				// print only, do not make the function to fail
				// continue to try other branches
				log.Debug(err.Error())
				isWaitingForConfirmation = true
				continue
			}

			return nil, err
		}

		if _, ok := transactionsMap[nextTx]; !ok {
			transactions = append(transactions, nextTx)
			transactionsMap[nextTx] = struct{}{}
		}
	}

	if len(transactions) == 0 {
		if isWaitingForConfirmation {
			return nil, ErrWaitingForConfirmation
		}

		return nil, nil
	}

	res := make([]UnrollRes, 0, len(transactions))
	for _, parent := range transactions {
		var parentTx wire.MsgTx
		if err := parentTx.Deserialize(hex.NewDecoder(strings.NewReader(parent))); err != nil {
			// Return the packages already broadcast alongside the error:
			// this loop broadcasts sequentially, so on a mid-loop failure
			// earlier packages are already in the mempool. Discarding
			// them (returning nil) hides live branch txs from the caller,
			// which then can't track the in-flight unroll.
			return res, err
		}

		childTxid, child, err := bumpAnchorTx(ctx, args, &parentTx)
		if err != nil {
			return res, err
		}

		// broadcast the package (parent + child)
		packageResponse, err := args.Explorer.Broadcast(parent, child)
		if err != nil {
			return res, err
		}

		res = append(res, UnrollRes{
			ParentTx:   parent,
			ParentTxid: parentTx.TxID(),
			ChildTx:    child,
			ChildTxid:  childTxid,
		})
		log.Debugf("package broadcasted: %s", packageResponse)
	}

	return res, nil
}

// BumpUnroll rebuilds the fee-bumping child of every branch transaction
// currently sitting unconfirmed in the mempool and rebroadcasts the package.
//
// Unroll pays for each branch tx with a CPFP child sized at the feerate that
// held when it broadcast, and then never revisits it: a branch already in the
// mempool makes NextRedeemTx return ErrPendingConfirmation, which Unroll takes
// as "wait". If the feerate rises after broadcast, the package can therefore
// sit unconfirmed indefinitely while the exit's CSV runs down. That is a race
// the user has to win — the server's pre-signed forfeit paths become spendable
// when it elapses — so waiting quietly is the wrong default.
//
// Calling this rebuilds each child at the current feerate and rebroadcasts.
// The new child spends the same P2A anchor as the old one and pays more, so it
// replaces it. TRUC/P2A relay policy allows exactly this: one replacement
// child per unconfirmed parent.
//
// It returns the packages it rebroadcast, empty when nothing is pending (i.e.
// nothing is stuck). Callers should drive it from a stall heuristic — a
// package that has been unconfirmed for longer than some threshold — rather
// than on every tick, since each call re-selects onchain UTXOs and pays a
// higher fee than the last.
func BumpUnroll(ctx context.Context, args UnrollArgs) ([]UnrollRes, error) {
	if err := args.validate(); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	branches, err := getBranchesToUnroll(
		ctx, args.Explorer, args.Indexer, args.Vtxos,
	)
	if err != nil {
		return nil, err
	}

	// Collect the pending parents, deduplicated: several vtxos can share a
	// branch, and bumping the same parent twice would have the second bump
	// replace the first for no gain.
	pendingTxids := make([]string, 0, len(branches))
	seen := make(map[string]struct{}, len(branches))
	for _, branch := range branches {
		_, err := branch.NextRedeemTx()
		pending, ok := err.(ErrPendingConfirmation)
		if !ok {
			continue
		}
		if _, dup := seen[pending.Txid]; dup {
			continue
		}
		seen[pending.Txid] = struct{}{}
		pendingTxids = append(pendingTxids, pending.Txid)
	}

	res := make([]UnrollRes, 0, len(pendingTxids))
	for _, txid := range pendingTxids {
		parent, err := args.Explorer.GetTxHex(txid)
		if err != nil {
			return res, fmt.Errorf("fetch pending tx %s: %w", txid, err)
		}

		var parentTx wire.MsgTx
		if err := parentTx.Deserialize(hex.NewDecoder(strings.NewReader(parent))); err != nil {
			return res, err
		}

		childTxid, child, err := bumpAnchorTx(ctx, args, &parentTx)
		if err != nil {
			return res, err
		}

		// Rebroadcasting the parent alongside the child is required by
		// package relay and harmless when the parent is already in the
		// mempool: it is accepted as a duplicate rather than rejected.
		packageResponse, err := args.Explorer.Broadcast(parent, child)
		if err != nil {
			return res, err
		}

		res = append(res, UnrollRes{
			ParentTx:   parent,
			ParentTxid: parentTx.TxID(),
			ChildTx:    child,
			ChildTxid:  childTxid,
		})
		log.Debugf("bumped package rebroadcast: %s", packageResponse)
	}

	return res, nil
}

// bumpAnchorTx builds and signs a transaction bumping the fees for a given tx with P2A output.
// Makes use of args.BumpAddr/args.BumpPubKey to select UTXOs to pay fees for parent.
func bumpAnchorTx(
	ctx context.Context, args UnrollArgs, parent *wire.MsgTx,
) (string, string, error) {
	anchor, err := txutils.FindAnchorOutpoint(parent)
	if err != nil {
		return "", "", err
	}

	// estimate for the size of the bump transaction
	weightEstimator := input.TxWeightEstimator{}

	// WeightEstimator doesn't support P2A size, using P2WSH will lead to a small overestimation
	// TODO use the exact P2A size
	weightEstimator.AddNestedP2WSHInput(lntypes.VByte(3).ToWU())

	// We assume only one UTXO will be selected to have a correct estimation
	weightEstimator.AddTaprootKeySpendInput(txscript.SigHashDefault)
	weightEstimator.AddP2TROutput()

	childVSize := weightEstimator.Weight().ToVB()

	packageSize := childVSize + computeVSize(parent)
	feeRate, err := args.Explorer.GetFeeRate()
	if err != nil {
		return "", "", err
	}

	feeAmount := math.Ceil(float64(packageSize) * feeRate)
	if math.IsNaN(feeAmount) || feeAmount < 0 || feeAmount >= float64(math.MaxInt64) {
		return "", "", fmt.Errorf("invalid fee amount %v", feeAmount)
	}
	fees := uint64(feeAmount)

	addr := args.BumpAddr
	pkScript, err := toOutputScript(addr, args.ServerParams.Network)
	if err != nil {
		return "", "", err
	}

	selectedCoins := make([]clientlib.ExplorerUtxo, 0)
	selectedAmount := uint64(0)
	amountToSelect := int64(fees) - txutils.ANCHOR_VALUE

	utxos, err := args.Explorer.GetUtxos([]string{addr})
	if err != nil {
		return "", "", err
	}

	for _, utxo := range utxos {
		selectedCoins = append(selectedCoins, utxo)
		selectedAmount += utxo.Amount
		amountToSelect -= int64(utxo.Amount)
		if amountToSelect <= 0 {
			break
		}
	}

	if amountToSelect > 0 {
		// A unilateral exit can't proceed without onchain funds to bump
		// its tree; distinguish "nothing at all" from "not quite enough"
		// so the caller (and eventually a fee sponsor) can react.
		if selectedAmount == 0 {
			return "", "", fmt.Errorf(
				"%w: none available", ErrInsufficientOnchainFunds,
			)
		}
		return "", "", fmt.Errorf(
			"%w: need %d more sat", ErrInsufficientOnchainFunds,
			amountToSelect,
		)
	}

	if fees > selectedAmount {
		return "", "", fmt.Errorf("not enough funds to cover network fees")
	}

	changeAmount := selectedAmount - fees

	inputs := []*wire.OutPoint{anchor}
	sequences := []uint32{
		wire.MaxTxInSequenceNum,
	}
	outputs := []*wire.TxOut{
		{
			Value:    int64(changeAmount),
			PkScript: pkScript,
		},
	}

	for _, utxo := range selectedCoins {
		txid, err := chainhash.NewHashFromStr(utxo.Txid)
		if err != nil {
			return "", "", err
		}
		inputs = append(inputs, &wire.OutPoint{
			Hash:  *txid,
			Index: utxo.Vout,
		})
		sequences = append(sequences, wire.MaxTxInSequenceNum)
	}

	ptx, err := psbt.New(inputs, outputs, 3, 0, sequences)
	if err != nil {
		return "", "", err
	}

	ptx.Inputs[0].WitnessUtxo = parent.TxOut[anchor.Index]

	for i, utxo := range selectedCoins {
		pkScript, err := hex.DecodeString(utxo.Script)
		if err != nil {
			return "", "", err
		}

		ptx.Inputs[i+1].WitnessUtxo = &wire.TxOut{
			Value:    int64(utxo.Amount),
			PkScript: pkScript,
		}
		ptx.Inputs[i+1].TaprootInternalKey = schnorr.SerializePubKey(args.BumpPubKey)
	}

	b64, err := ptx.B64Encode()
	if err != nil {
		return "", "", err
	}

	tx, err := args.SignTx(ctx, b64)
	if err != nil {
		return "", "", err
	}

	signedPtx, err := psbt.NewFromRawBytes(strings.NewReader(tx), true)
	if err != nil {
		return "", "", err
	}

	for inIndex := range signedPtx.Inputs[1:] {
		if _, err := psbt.MaybeFinalize(signedPtx, inIndex+1); err != nil {
			return "", "", err
		}
	}

	childTx, err := txutils.ExtractWithAnchors(signedPtx)
	if err != nil {
		return "", "", err
	}

	var serializedTx bytes.Buffer
	if err := childTx.Serialize(&serializedTx); err != nil {
		return "", "", err
	}

	return childTx.TxID(), hex.EncodeToString(serializedTx.Bytes()), nil
}
