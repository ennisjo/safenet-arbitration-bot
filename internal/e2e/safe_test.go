package e2e_test

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// safeOutput is the JSON output of `arbot safe`.
type safeOutput struct {
	Safe            ethrpc.Address   `json:"safe"`
	ChainID         uint64           `json:"chainId"`
	SafeBlock       uint64           `json:"safeBlock"`
	Singleton       ethrpc.Address   `json:"singleton"`
	Owners          []ethrpc.Address `json:"owners"`
	OwnerCount      *big.Int         `json:"ownerCount"`
	Threshold       *big.Int         `json:"threshold"`
	Nonce           *big.Int         `json:"nonce"`
	Modules         []ethrpc.Address `json:"modules"`
	FallbackHandler ethrpc.Address   `json:"fallbackHandler"`
	Guard           ethrpc.Address   `json:"guard"`
	ModuleGuard     ethrpc.Address   `json:"moduleGuard"`
}

// TestSafe runs `arbot safe` on a Safe 1.3.0 that the singleton's setup set up
// on the Mainnet node, with a guard set before the proposal, and a module guard
// set after it, which the evidence must not include.
func TestSafe(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	if r.Transaction.ChainID.Uint64() != ethrpc.Mainnet {
		t.Fatalf("Safe transaction is for chain %s, want Ethereum Mainnet", r.Transaction.ChainID)
	}
	mainnet, safe := n.Mainnet, r.Transaction.Safe
	owner, other := ethrpc.Address{0: 0x0e, 19: 1}, ethrpc.Address{0: 0x0e, 19: 2}
	guard, moduleGuard := ethrpc.Address{0: 0x6a, 19: 1}, ethrpc.Address{0: 0x36, 19: 1}
	// set sets the setting at the hash of name to address, as the singleton would.
	set := func(name string, address ethrpc.Address) {
		var value ethrpc.Hash
		copy(value[12:], address[:])
		mainnet.RPC(nil, "anvil_setStorageAt", safe, ethrpc.Hash(keccak256.Hash([]byte(name))), value)
	}

	// The Mainnet node's blocks are earlier than the proposal until it mines up to
	// it.
	mainnet.SetBalance(owner, new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil))
	n.SetupSafe(mainnet, safe, singleton130, owner, other)
	set("guard_manager.guard.address", guard)
	mainnet.MineUntil(n.Header(r.Block).Time())
	set("module_manager.module_guard.address", moduleGuard)
	mainnet.MineUntil(n.Header(r.Block).Time().Add(time.Minute))

	arbot := n.Arbot()
	info := arbot.Run(t, "info", "-json", r.ID.String())
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, info, 0o644); err != nil {
		t.Fatal(err)
	}
	safeBlock := decode[requestInfo](t, info).Proposal.SafeBlock

	got := decode[safeOutput](t, arbot.Run(t, "safe", "-json", "-request-file", path))
	want := safeOutput{
		Safe: safe, ChainID: ethrpc.Mainnet, SafeBlock: safeBlock, Singleton: singleton130,
		Owners: []ethrpc.Address{owner, other}, OwnerCount: big.NewInt(2), Threshold: big.NewInt(1), Nonce: new(big.Int),
		Modules: []ethrpc.Address{}, Guard: guard,
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("safe:\ngot  %s\nwant %s", g, w)
	}

	t.Run("text", func(t *testing.T) {
		text := arbot.Run(t, "safe", "-request-file", path)
		for _, line := range []string{
			`Safe ` + safe.String() + ` on chain 1, as of block \d+`,
			`  Singleton         ` + singleton130.String(),
			`  Threshold         1 of 2 owners`,
			`  Guard             ` + guard.String(),
			`  ` + other.String(),
			`  none`,
		} {
			if !regexp.MustCompile(`(?m)^` + line + `$`).Match(text) {
				t.Errorf("safe: output has no line matching %q:\n%s", line, text)
			}
		}
	})

	t.Run("missing request file", func(t *testing.T) {
		arbot.Fail(t, 2, "safe")
	})
}
