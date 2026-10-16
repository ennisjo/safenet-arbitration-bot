package e2e

import (
	"bufio"
	"fmt"
	"io"
	"math/big"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// Anvil is a local anvil node, which impersonates any account that sends it a
// transaction. It embeds a client for the node, and adds anvil's own methods.
type Anvil struct {
	*ethrpc.Client
	tb  testing.TB
	URL string
	// BlockTime is the time between blocks, in seconds, which is that of the chain
	// whose ID the node has.
	BlockTime uint64
}

// blockTimes are the times between blocks, in seconds, of the chains whose IDs
// anvil nodes can have.
var blockTimes = map[uint64]uint64{ethrpc.Mainnet: 12, ethrpc.Gnosis: 5}

// StartAnvil starts an anvil node for the chain with the given ID, which runs
// until the test ends, passing args to anvil. It skips the test if anvil isn't
// installed, or in short mode.
//
// The node only keeps the state of the latest block, so calls at older blocks
// fail, unless args has its own --prune-history=N flag, which keeps the states
// of the last N blocks. Keeping history makes mining several times slower, as
// anvil snapshots the state of every block.
func StartAnvil(tb testing.TB, chainID uint64, args ...string) *Anvil {
	tb.Helper()
	if testing.Short() {
		tb.Skip("skipping end-to-end test in short mode")
	}
	path, err := exec.LookPath("anvil")
	if err != nil {
		tb.Skip("skipping end-to-end test: anvil is not installed")
	}

	blockTime, ok := blockTimes[chainID]
	if !ok {
		tb.Fatalf("no block time for chain %d", chainID)
	}
	prune := "--prune-history"
	if slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "--prune-history") }) {
		prune = ""
	}
	args = append([]string{"--chain-id", fmt.Sprint(chainID), "--port", "0", "--auto-impersonate", prune}, args...)
	args = slices.DeleteFunc(args, func(arg string) bool { return arg == "" })
	// The test's context is canceled before its cleanup functions run, which kills
	// anvil.
	cmd := exec.CommandContext(tb.Context(), path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		tb.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		tb.Fatalf("starting anvil: %v", err)
	}
	tb.Cleanup(func() { cmd.Wait() })

	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		if address, ok := strings.CutPrefix(lines.Text(), "Listening on "); ok {
			go io.Copy(io.Discard, stdout)
			url := "http://" + address
			eth, err := ethrpc.NewClient(tb.Context(), chainID, url)
			if err != nil {
				tb.Fatal(err)
			}
			a := &Anvil{Client: eth, tb: tb, URL: url, BlockTime: blockTime}
			// Space all blocks BlockTime seconds apart, like the chain's, rather than at
			// the time they are mined, which can give many blocks the same time.
			a.rpc(nil, "anvil_setBlockTimestampInterval", blockTime)
			return a
		}
	}
	tb.Fatalf("anvil exited without listening: %v", lines.Err())
	return nil
}

// Install sets the code and storage of the artifacts' accounts.
func (a *Anvil) Install(artifacts *Artifacts) {
	a.tb.Helper()
	for address, account := range artifacts.Accounts {
		a.rpc(nil, "anvil_setCode", address, account.Code)
		for slot, value := range account.Storage {
			a.rpc(nil, "anvil_setStorageAt", address, slot, value)
		}
	}
}

// rpc calls one of anvil's methods with params, and decodes its result into
// result unless it is nil. It fails the test on error.
func (a *Anvil) rpc(result any, method string, params ...any) {
	a.tb.Helper()
	if err := a.RawRequest(a.tb.Context(), result, method, params...); err != nil {
		a.tb.Fatal(err)
	}
}

// RPC calls one of anvil's methods with params, and decodes its result into
// result unless it is nil. It fails the test on error.
func (a *Anvil) RPC(result any, method string, params ...any) {
	a.tb.Helper()
	a.rpc(result, method, params...)
}

// blockNumber returns the number of the latest block.
func (a *Anvil) blockNumber() ethrpc.BlockNumber {
	a.tb.Helper()
	n, err := a.BlockNumber(a.tb.Context())
	if err != nil {
		a.tb.Fatal(err)
	}
	return n
}

// Header returns the header of block number.
func (a *Anvil) Header(number uint64) ethrpc.Block {
	a.tb.Helper()
	block, err := a.GetBlockByNumber(a.tb.Context(), ethrpc.BlockNumber(number))
	if err != nil {
		a.tb.Fatal(err)
	}
	return block
}

// Mine mines n empty blocks, BlockTime seconds apart.
func (a *Anvil) Mine(n uint64) {
	a.tb.Helper()
	a.rpc(nil, "anvil_mine", ethrpc.Quantity(n), ethrpc.Quantity(a.BlockTime))
}

// untilBlocks is the number of blocks before t that MineUntil spaces BlockTime
// seconds apart, which is more than any node keeps the states of.
const untilBlocks = 256

// MineUntil mines empty blocks until the latest block is at or after t. The
// last untilBlocks blocks before t are BlockTime seconds apart, and a single
// block skips the time before them, since mining every block of a long time is
// slow.
func (a *Anvil) MineUntil(t time.Time) {
	a.tb.Helper()
	latest := a.Header(uint64(a.blockNumber())).Time()
	if !latest.Before(t) {
		return
	}
	if skip := t.Add(-untilBlocks * time.Duration(a.BlockTime) * time.Second); latest.Before(skip) {
		a.rpc(nil, "evm_setNextBlockTimestamp", ethrpc.Quantity(skip.Unix()))
		a.rpc(nil, "evm_mine")
		latest = skip
	}
	a.Mine(uint64(t.Sub(latest)/time.Second)/a.BlockTime + 1)
}

// MineTo mines empty blocks until block is the latest block.
func (a *Anvil) MineTo(block uint64) {
	a.tb.Helper()
	if latest := uint64(a.blockNumber()); latest < block {
		a.Mine(block - latest)
	}
}

// SetAutomine sets whether anvil mines each transaction in its own block as it
// is sent. Otherwise, it mines them when MinePending is called.
func (a *Anvil) SetAutomine(automine bool) {
	a.tb.Helper()
	a.rpc(nil, "evm_setAutomine", automine)
}

// MinePending mines a block with the pending transactions.
func (a *Anvil) MinePending() {
	a.tb.Helper()
	a.rpc(nil, "evm_mine")
}

// SetBalance sets the ether balance of account.
func (a *Anvil) SetBalance(account ethrpc.Address, wei *big.Int) {
	a.tb.Helper()
	a.rpc(nil, "anvil_setBalance", account, "0x"+wei.Text(16))
}

// Receipt is a transaction receipt.
type Receipt struct {
	TransactionHash ethrpc.Hash        `json:"transactionHash"`
	BlockNumber     ethrpc.BlockNumber `json:"blockNumber"`
	Status          ethrpc.Quantity    `json:"status"`
	GasUsed         ethrpc.Quantity    `json:"gasUsed"`
	Logs            []ethrpc.Log       `json:"logs"`
}

// Log returns the receipt's log of event emitted by address, if any.
func (r *Receipt) Log(address ethrpc.Address, event ethrpc.Hash) (ethrpc.Log, bool) {
	i := slices.IndexFunc(r.Logs, func(log ethrpc.Log) bool {
		return log.Address == address && len(log.Topics) > 0 && log.Topics[0] == event
	})
	if i < 0 {
		return ethrpc.Log{}, false
	}
	return r.Logs[i], true
}

// Send sends a transaction from from, without waiting for it to be mined, and
// returns its hash.
func (a *Anvil) Send(from, to ethrpc.Address, data ethrpc.Bytes, value *big.Int) ethrpc.Hash {
	a.tb.Helper()
	tx := map[string]any{"from": from, "to": to, "data": data}
	if value != nil {
		tx["value"] = "0x" + value.Text(16)
	}
	var hash ethrpc.Hash
	a.rpc(&hash, "eth_sendTransaction", tx)
	return hash
}

// Receipt returns the receipt of the transaction hash, waiting for anvil to
// mine it, and fails the test if it reverted.
func (a *Anvil) Receipt(hash ethrpc.Hash) *Receipt {
	a.tb.Helper()
	var r *Receipt
	for range 100 {
		if a.rpc(&r, "eth_getTransactionReceipt", hash); r != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r == nil {
		a.tb.Fatalf("transaction %s is not mined", hash)
	}
	if r.Status != 1 {
		a.tb.Fatalf("transaction %s reverted: %s", hash, a.revert(r))
	}
	return r
}

// revert returns what is known of why the transaction of receipt r reverted:
// how much of its gas limit it used, as a transaction that used all of it ran
// out of gas, and the error of a trace of it, if the node can trace it. A node
// without history can't, as it needs the state before the block, as replaying
// it with eth_call would.
func (a *Anvil) revert(r *Receipt) string {
	a.tb.Helper()
	var tx struct {
		Gas ethrpc.Quantity `json:"gas"`
	}
	a.rpc(&tx, "eth_getTransactionByHash", r.TransactionHash)
	reason := fmt.Sprintf("used %d of its %d gas", r.GasUsed, tx.Gas)
	var trace struct {
		Error        string       `json:"error"`
		RevertReason string       `json:"revertReason"`
		Output       ethrpc.Bytes `json:"output"`
	}
	params := map[string]any{"tracer": "callTracer"}
	if err := a.RawRequest(a.tb.Context(), &trace, "debug_traceTransaction", r.TransactionHash, params); err != nil {
		return fmt.Sprintf("%s, and can't be traced: %v", reason, err)
	}
	return fmt.Sprintf("%s, with error %q, reason %q, and output %s", reason, trace.Error, trace.RevertReason, trace.Output)
}

// Transact sends a transaction, waits for it to be mined, and returns its
// receipt.
func (a *Anvil) Transact(from, to ethrpc.Address, data ethrpc.Bytes) *Receipt {
	a.tb.Helper()
	return a.Receipt(a.Send(from, to, data, nil))
}
