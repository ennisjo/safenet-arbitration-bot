// Command fetch-artifacts fetches the code of the default Safenet deployment on
// Gnosis Chain, for the end-to-end tests in internal/e2e to run it on anvil.
//
// Usage:
//
//	go run ./internal/cmd/fetch-artifacts [-o file]
//
// It writes the accounts to install on an anvil node with anvil_setCode and
// anvil_setStorageAt, as JSON, to the file or stdout:
//
//   - The SentinelOracle, Consensus, and FROST coordinator contracts, at their
//     addresses. Their immutables, such as the oracle's windows and arbitrator,
//     are part of their code.
//   - WETH9 from Ethereum Mainnet, at the address of the oracle's fee token, so
//     that anyone can mint fee tokens by depositing ether.
//   - The Safe 1.3.0 and SafeL2 1.3.0 singletons from Ethereum Mainnet, at their
//     deterministic addresses, so that Safes on anvil run the real contracts.
//
// Contracts start out with empty storage on anvil, so it also writes the
// storage they need to work. It sets fixed values rather than copying the
// deployment's storage, so that the end-to-end tests always run with the same
// configuration:
//
//   - The oracle's fee, bond, DAO fee share, and protocol funds receiver
//     configuration, and its Charter ENS name.
//   - Sentinels, active from block 1.
//   - An active Consensus epoch, whose coordinator group is finalized with the
//     secp256k1 generator as its key, so its private key is 1.
//   - The WETH9 name, symbol, and decimals.
//
// The storage slots are those that `forge inspect <contract> storageLayout`
// reports for the Safenet contracts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var progname = filepath.Base(os.Args[0])

// weth9 is the address of WETH9 on Ethereum Mainnet.
var weth9 = ethrpc.MustParseAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")

// The Safe 1.3.0 singletons, at their addresses on every chain.
var (
	safeSingleton   = ethrpc.MustParseAddress("0xd9Db270c1B5E3Bd161E8c8503c55cEABeE709552")
	safeL2Singleton = ethrpc.MustParseAddress("0x3E5c63644E683549055b9Be8653de26E0B4CD36E")
)

// The Consensus epoch and coordinator group that the artifacts set up. Group
// IDs have their low 64 bits clear, as signature IDs keep the sequence there.
const epoch = 42

var groupID = solabi.Uint(new(big.Int).Lsh(big.NewInt(1), 64))

// sentinels are the sentinels that the artifacts register with the oracle.
var sentinels = []ethrpc.Address{
	ethrpc.MustParseAddress("0x5e00000000000000000000000000000000000001"),
	ethrpc.MustParseAddress("0x5e00000000000000000000000000000000000002"),
	ethrpc.MustParseAddress("0x5e00000000000000000000000000000000000003"),
}

// The oracle configuration that the artifacts set up. The DAO fee share is in
// units of 1/100,000.
var (
	fee                   = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	protocolFundsReceiver = ethrpc.MustParseAddress("0xfee0000000000000000000000000000000000001")
)

const (
	bondMultiplier     = 10
	slashingMultiplier = 1
	daoFeeShare        = 10_000
	charterENS         = "charter.safenet-gov.eth"
)

// The coordinates of the secp256k1 generator.
var (
	generatorX = mustParseHash("0x79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	generatorY = mustParseHash("0x483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8")
)

// Artifacts are the accounts to install on anvil, and the addresses that the
// end-to-end tests need.
type Artifacts struct {
	Oracle      ethrpc.Address `json:"oracle"`
	Consensus   ethrpc.Address `json:"consensus"`
	Coordinator ethrpc.Address `json:"coordinator"`
	FeeToken    ethrpc.Address `json:"feeToken"`
	Arbitrator  ethrpc.Address `json:"arbitrator"`
	// SafeSingleton and SafeL2Singleton are the Safe 1.3.0 singletons, which have
	// code but no storage.
	SafeSingleton   ethrpc.Address   `json:"safeSingleton"`
	SafeL2Singleton ethrpc.Address   `json:"safeL2Singleton"`
	Sentinels       []ethrpc.Address `json:"sentinels"`
	Epoch           uint64           `json:"epoch"`
	GroupID         ethrpc.Hash      `json:"groupId"`

	Accounts map[ethrpc.Address]*Account `json:"accounts"`
}

// Account is the code and storage of an account.
type Account struct {
	Code    ethrpc.Bytes                `json:"code"`
	Storage map[ethrpc.Hash]ethrpc.Hash `json:"storage"`
}

func main() {
	output := flag.String("o", "", "file to write the artifacts to (default: stdout)")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage: %s [-o file]\n\n", progname)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	artifacts, err := fetch(ctx)
	if err != nil {
		die("%v", err)
	}

	var out io.Writer = os.Stdout
	if *output != "" {
		file, err := os.Create(*output)
		if err != nil {
			die("%v", err)
		}
		defer file.Close()
		out = file
	}
	// Write the artifacts as `jq .` formats them, which `just check-testdata`
	// checks.
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(artifacts); err != nil {
		die("%v", err)
	}
}

func fetch(ctx context.Context) (*Artifacts, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	mainnet, err := connect(ctx, cfg, ethrpc.Mainnet)
	if err != nil {
		return nil, err
	}
	gnosis, err := connect(ctx, cfg, ethrpc.Gnosis)
	if err != nil {
		return nil, err
	}

	a := &Artifacts{
		Oracle:          safenet.DefaultOracle,
		Consensus:       safenet.DefaultConsensus,
		SafeSingleton:   safeSingleton,
		SafeL2Singleton: safeL2Singleton,
		Sentinels:       sentinels,
		Epoch:           epoch,
		GroupID:         groupID,
		Accounts:        make(map[ethrpc.Address]*Account),
	}
	proposer, err := gnosis.callAddress(ctx, a.Oracle, "PROPOSER()")
	if err != nil {
		return nil, err
	}
	if proposer != a.Consensus {
		return nil, fmt.Errorf("SentinelOracle %s has PROPOSER %s, not Consensus %s", a.Oracle, proposer, a.Consensus)
	}
	if a.Coordinator, err = gnosis.callAddress(ctx, a.Consensus, "getCoordinator()"); err != nil {
		return nil, err
	}
	if a.FeeToken, err = gnosis.callAddress(ctx, a.Oracle, "FEE_TOKEN()"); err != nil {
		return nil, err
	}
	if a.Arbitrator, err = gnosis.callAddress(ctx, a.Oracle, "ARBITRATOR()"); err != nil {
		return nil, err
	}

	oracle, err := gnosis.account(ctx, a.Oracle)
	if err != nil {
		return nil, err
	}
	// Slots 0 to 4 hold the bond, protocol funds receiver, fee, and DAO fee share
	// configuration. Each is a current value and a pending change, which is left
	// unscheduled. The bond configuration packs its multipliers after the 8-byte
	// block that its pending change activates at. Slot 8 holds the Charter ENS
	// name.
	bond := new(big.Int).Lsh(big.NewInt(slashingMultiplier), 32)
	bond.Or(bond, big.NewInt(bondMultiplier))
	oracle.Storage[slot(0)] = solabi.Uint(bond.Lsh(bond, 64))
	oracle.Storage[slot(1)] = solabi.Address(protocolFundsReceiver)
	oracle.Storage[slot(3)] = solabi.Uint(fee)
	oracle.Storage[slot(4)] = solabi.Uint64(daoFeeShare)
	oracle.Storage[slot(8)] = shortString(charterENS)
	// The sentinel map's schedule, in slot 5, maps sentinels to the block they are
	// active from.
	for _, sentinel := range a.Sentinels {
		oracle.Storage[mappingSlot(solabi.Address(sentinel), 5)] = solabi.Uint64(1)
	}
	a.Accounts[a.Oracle] = oracle

	consensus, err := gnosis.account(ctx, a.Consensus)
	if err != nil {
		return nil, err
	}
	// Slot 0 holds the epochs, packed as previous, active, staged, and rollover
	// block, from the lowest-order bytes. Slot 1 maps epochs to their groups.
	consensus.Storage[slot(0)] = solabi.Uint(new(big.Int).Lsh(big.NewInt(epoch), 64))
	consensus.Storage[mappingSlot(solabi.Uint64(epoch), 1)] = a.GroupID
	a.Accounts[a.Consensus] = consensus

	coordinator, err := gnosis.account(ctx, a.Coordinator)
	if err != nil {
		return nil, err
	}
	// Slot 0 maps group IDs to groups. A group's state is in its fifth slot:
	// status, count, and threshold, packed from the lowest-order byte, with status
	// 5 being FINALIZED. Its key's coordinates follow.
	group := mappingSlot(a.GroupID, 0)
	const finalized, count, threshold = 5, 2, 2
	coordinator.Storage[offset(group, 4)] = solabi.Uint64(finalized | count<<8 | threshold<<24)
	coordinator.Storage[offset(group, 5)] = generatorX
	coordinator.Storage[offset(group, 6)] = generatorY
	a.Accounts[a.Coordinator] = coordinator

	token, err := mainnet.account(ctx, weth9)
	if err != nil {
		return nil, err
	}
	// Slots 0 to 2 hold the name, symbol, and decimals.
	token.Storage[slot(0)] = shortString("Wrapped Ether")
	token.Storage[slot(1)] = shortString("WETH")
	token.Storage[slot(2)] = solabi.Uint64(18)
	a.Accounts[a.FeeToken] = token

	for _, singleton := range []ethrpc.Address{safeSingleton, safeL2Singleton} {
		if a.Accounts[singleton], err = mainnet.account(ctx, singleton); err != nil {
			return nil, err
		}
	}

	return a, nil
}

// chain is a client for a chain, reading as of a fixed block.
type chain struct {
	eth   *ethrpc.Client
	block ethrpc.BlockNumber
}

func connect(ctx context.Context, cfg config.Config, chainID uint64) (*chain, error) {
	eth, err := ethrpc.NewClient(ctx, chainID, cfg.RPCs[chainID])
	if err != nil {
		return nil, err
	}
	block, err := eth.BlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	return &chain{eth: eth, block: block}, nil
}

// callAddress calls a function of the contract at to, without arguments, and
// decodes the address it returns.
func (c *chain) callAddress(ctx context.Context, to ethrpc.Address, signature string) (ethrpc.Address, error) {
	call := ethrpc.CallRequest{To: to, Data: solabi.Call(solabi.Selector(signature))}
	result, err := c.eth.Call(ctx, call, c.block)
	if err != nil {
		return ethrpc.Address{}, fmt.Errorf("calling %s on %s: %w", signature, to, err)
	}
	d := solabi.NewDecoder(result)
	address := d.Address(0)
	if err := d.Err(); err != nil {
		return ethrpc.Address{}, fmt.Errorf("calling %s on %s: %w", signature, to, err)
	}
	return address, nil
}

// account returns an account with the code of the contract at address, and
// empty storage.
func (c *chain) account(ctx context.Context, address ethrpc.Address) (*Account, error) {
	code, err := c.eth.GetCode(ctx, address, c.block)
	if err != nil {
		return nil, fmt.Errorf("getting code of %s: %w", address, err)
	}
	if len(code) == 0 {
		return nil, fmt.Errorf("no contract at %s", address)
	}
	return &Account{Code: code, Storage: make(map[ethrpc.Hash]ethrpc.Hash)}, nil
}

func slot(n uint64) ethrpc.Hash {
	return solabi.Uint64(n)
}

// mappingSlot returns the slot of the value for key in the mapping at slot n.
func mappingSlot(key solabi.Word, n uint64) ethrpc.Hash {
	s := slot(n)
	return keccak256.Hash(key[:], s[:])
}

// shortString returns the storage slot of a string of up to 31 bytes, which
// holds the string followed by twice its length in the lowest-order byte.
func shortString(s string) ethrpc.Hash {
	var value ethrpc.Hash
	if len(s) >= len(value) {
		panic(fmt.Sprintf("string %q is too long to be stored in its slot", s))
	}
	copy(value[:], s)
	value[31] = byte(2 * len(s))
	return value
}

// offset returns the slot i slots after s.
func offset(s ethrpc.Hash, i uint64) ethrpc.Hash {
	sum := new(big.Int).SetBytes(s[:])
	return solabi.Uint(sum.Add(sum, new(big.Int).SetUint64(i)))
}

func mustParseHash(s string) ethrpc.Hash {
	hash, err := ethrpc.ParseHash(s)
	if err != nil {
		panic(err)
	}
	return hash
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", progname, fmt.Sprintf(format, args...))
	os.Exit(1)
}
