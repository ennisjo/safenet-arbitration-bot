package checks

import (
	"context"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// networks are the chain IDs of the networks that the Charter's
// transaction-security rules apply to (Article I, Networks).
var networks = []uint64{ethrpc.Mainnet, ethrpc.ArbitrumOne, ethrpc.Gnosis}

// offNetwork finds a call by a Safe on a network that isn't in networks
// insecure, under R-4.0 of the Charter
// (bafkreigg6ajqdb52kb644wcxegurt7r6jlclru4mjy3lwxkbh6avm3vgre): a transaction
// is insecure if its chain ID isn't one of the networks of Article I, and such
// a transaction isn't out of scope (§ 3.9). R-4.0 is the only rule that applies
// on every network, so offNetwork runs first, and later checks only read the
// networks in networks.
var offNetwork = check{
	verdict:     Insecure,
	rule:        "R-4.0",
	description: "Safe on a network that the Charter doesn't cover",
	fn: func(_ context.Context, _ *env, safe *safeID, _ call) (bool, error) {
		return !slices.ContainsFunc(networks, func(n uint64) bool {
			return safe.chainID.IsUint64() && safe.chainID.Uint64() == n
		}), nil
	},
}
