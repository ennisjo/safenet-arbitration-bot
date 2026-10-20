// Package safesettings reads the settings of a Safe, as Charter § 2.10 lists
// them, from its storage.
package safesettings

import (
	"context"
	"fmt"
	"math/big"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// The storage slots of the Safe singletons of the versions that § 2.1 lists,
// 1.3.0, 1.4.1, and 1.5.0, which share their layout.
var (
	singletonSlot  = slot(0)
	modulesSlot    = slot(1)
	ownersSlot     = slot(2)
	ownerCountSlot = slot(3)
	thresholdSlot  = slot(4)
	nonceSlot      = slot(5)
	// The fallback handler, guard, and module guard are at the Keccak-256 hash of a
	// name, rather than in a slot of their own. Only 1.5.0 has a module guard.
	fallbackHandlerSlot = ethrpc.Hash(keccak256.Hash([]byte("fallback_manager.handler.address")))
	guardSlot           = ethrpc.Hash(keccak256.Hash([]byte("guard_manager.guard.address")))
	moduleGuardSlot     = ethrpc.Hash(keccak256.Hash([]byte("module_manager.module_guard.address")))
)

// sentinel is the address that starts and ends the linked lists of owners and
// modules.
var sentinel = ethrpc.Address{19: 1}

// maxListLength is the most entries that a list of owners or modules is read
// to, so that a malformed list that loops ends.
const maxListLength = 1024

// Settings are the settings of a Safe, as its storage has them. They are what
// the singleton's code reads, if the singleton is a Safe of a version that §
// 2.1 lists, and are meaningless otherwise.
type Settings struct {
	// Singleton is the address in the low 20 bytes of storage slot 0.
	Singleton ethrpc.Address `json:"singleton"`
	// Owners are the owners, in the order of their linked list.
	Owners []ethrpc.Address `json:"owners"`
	// OwnerCount is the number of owners that the Safe stores, which it keeps apart
	// from the list.
	OwnerCount *big.Int `json:"ownerCount"`
	Threshold  *big.Int `json:"threshold"`
	Nonce      *big.Int `json:"nonce"`
	// Modules are the enabled modules, in the order of their linked list.
	Modules         []ethrpc.Address `json:"modules"`
	FallbackHandler ethrpc.Address   `json:"fallbackHandler"`
	Guard           ethrpc.Address   `json:"guard"`
	// ModuleGuard is the module guard, which only Safe 1.5.0 has.
	ModuleGuard ethrpc.Address `json:"moduleGuard"`
}

// Read reads the settings of the Safe at safe from its storage at block. It
// returns an error if a list of owners or modules doesn't end, or ends without
// coming back to its start.
func Read(ctx context.Context, client *ethrpc.Client, safe ethrpc.Address, block ethrpc.BlockNumber) (*Settings, error) {
	r := reader{ctx: ctx, client: client, safe: safe, block: block}
	s := &Settings{
		Singleton:       r.address(singletonSlot),
		OwnerCount:      r.uint(ownerCountSlot),
		Threshold:       r.uint(thresholdSlot),
		Nonce:           r.uint(nonceSlot),
		FallbackHandler: r.address(fallbackHandlerSlot),
		Guard:           r.address(guardSlot),
		ModuleGuard:     r.address(moduleGuardSlot),
	}
	s.Owners = r.list("owners", ownersSlot)
	s.Modules = r.list("modules", modulesSlot)
	if r.err != nil {
		return nil, r.err
	}
	return s, nil
}

// reader reads a Safe's storage, keeping the first error.
type reader struct {
	ctx    context.Context
	client *ethrpc.Client
	safe   ethrpc.Address
	block  ethrpc.BlockNumber
	err    error
}

func (r *reader) word(slot ethrpc.Hash) ethrpc.Hash {
	if r.err != nil {
		return ethrpc.Hash{}
	}
	var value ethrpc.Hash
	value, r.err = r.client.GetStorageAt(r.ctx, r.safe, slot, r.block)
	return value
}

func (r *reader) address(slot ethrpc.Hash) ethrpc.Address {
	value := r.word(slot)
	return ethrpc.Address(value[12:])
}

func (r *reader) uint(slot ethrpc.Hash) *big.Int {
	value := r.word(slot)
	return new(big.Int).SetBytes(value[:])
}

// list reads the linked list of addresses in the mapping at slot, which maps
// sentinel to the first entry, each entry to the next, and the last one to
// sentinel. An empty list maps sentinel to sentinel, or to the zero address if
// it was never set up.
func (r *reader) list(name string, slot ethrpc.Hash) []ethrpc.Address {
	entries := []ethrpc.Address{}
	next := r.address(mappingSlot(sentinel, slot))
	if next == (ethrpc.Address{}) {
		return entries
	}
	for next != sentinel && r.err == nil {
		if next == (ethrpc.Address{}) {
			r.err = fmt.Errorf("the list of %s of %s ends without coming back to its start", name, r.safe)
			break
		}
		if len(entries) == maxListLength {
			r.err = fmt.Errorf("the list of %s of %s has more than %d entries", name, r.safe, maxListLength)
			break
		}
		entries = append(entries, next)
		next = r.address(mappingSlot(next, slot))
	}
	return entries
}

// slot returns the storage slot with index i.
func slot(i byte) ethrpc.Hash {
	return ethrpc.Hash{31: i}
}

// mappingSlot returns the storage slot of the entry for key in a Solidity
// mapping from addresses at slot.
func mappingSlot(key ethrpc.Address, slot ethrpc.Hash) ethrpc.Hash {
	var preimage [64]byte
	copy(preimage[12:32], key[:])
	copy(preimage[32:], slot[:])
	return ethrpc.Hash(keccak256.Hash(preimage[:]))
}
