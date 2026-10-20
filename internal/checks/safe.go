package checks

import (
	"context"
	"slices"
	"sync"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// safeProxy is the SafeProxy contract of a proxy version that § 2.1 lists, and
// the networks whose official proxy factory of that version deploys it.
type safeProxy struct {
	version  string
	networks []uint64
}

// safeProxyCodeHashes maps the Keccak-256 hash of the code of each SafeProxy
// contract of the proxy versions that § 2.1 lists, 1.0.0, 1.1.1, 1.3.0, 1.4.1,
// and 1.5.0, to its version and the networks whose official proxy factory of
// that version deploys it. The code is the factory's proxyRuntimeCode for
// 1.0.0, 1.1.1, and 1.3.0, which is the code of the proxies that it creates. A
// proxy has the same code however it was deployed, since its code doesn't
// depend on its singleton, which it keeps in storage.
//
// The 1.0.0 and 1.1.1 proxy factories are only deployed on Ethereum Mainnet and
// Gnosis Chain. The 1.1.1 factory has the same address on both, but its code on
// Gnosis Chain was compiled with other metadata, so its proxies differ from
// those on Ethereum Mainnet. Safes of 1.2.0, which shipped no proxy factory,
// use the 1.1.1 proxy.
//
// Each hash can be checked against one of two sources, given above it. One is
// the deployedBytecode of the proxy in the build artifacts of the version's npm
// release, at https://unpkg.com/<package>@<version>/<path>. The other is the
// proxyRuntimeCode of the version's official proxy factory, as the
// safe-deployments repository lists it, on the network. The 1.0.0 artifact
// matches no deployed proxy, nor does the 1.1.1 artifact match the proxies on
// Gnosis Chain, so those hashes come from the factories only.
var safeProxyCodeHashes = map[ethrpc.Hash]safeProxy{
	// proxyRuntimeCode of the ProxyFactory
	// 0x12302fE9c02ff50939BaAaaf415fc226C078613C on Ethereum Mainnet and Gnosis
	// Chain.
	ethrpc.MustParseHash("0xf8fffcb9b9c73fbc4ffb6dc52f0001fa998951e0d4177894b1b899f7732b6eaf"): {"1.0.0", []uint64{ethrpc.Mainnet, ethrpc.Gnosis}},
	// @gnosis.pm/safe-contracts@1.1.1/build/contracts/Proxy.json, and
	// proxyRuntimeCode of the ProxyFactory
	// 0x76E2cFc1F5Fa8F6a5b3fC4c8F4788F0116861F9B on Ethereum Mainnet.
	ethrpc.MustParseHash("0xaea7d4252f6245f301e540cfbee27d3a88de543af8e49c5c62405d5499fab7e5"): {"1.1.1", []uint64{ethrpc.Mainnet}},
	// proxyRuntimeCode of the same ProxyFactory on Gnosis Chain.
	ethrpc.MustParseHash("0x30cf24338d14750aab3433d8bb0cedd0e1263ee1f4f5f84cc482f3541ce61ad5"): {"1.1.1", []uint64{ethrpc.Gnosis}},
	// @gnosis.pm/safe-contracts@1.3.0/build/artifacts/contracts/proxies/GnosisSafeProxy.sol/GnosisSafeProxy.json
	ethrpc.MustParseHash("0xb89c1b3bdf2cf8827818646bce9a8f6e372885f8c55e5c07acbd307cb133b000"): {"1.3.0", networks},
	// @safe-global/safe-contracts@1.4.1/build/artifacts/contracts/proxies/SafeProxy.sol/SafeProxy.json
	ethrpc.MustParseHash("0xd7d408ebcd99b2b70be43e20253d6d92a8ea8fab29bd3be7f55b10032331fb4c"): {"1.4.1", networks},
	// @safe-global/safe-smart-account@1.5.0/build/artifacts/contracts/proxies/SafeProxy.sol/SafeProxy.json
	ethrpc.MustParseHash("0x4e381985ca68b3e5d27b4425fa581c19cf33146d3f887a3cfca96f55528ea46f"): {"1.5.0", networks},
}

// proxyVersion returns the proxy version of § 2.1 whose official proxy factory
// deploys a SafeProxy with code on the network with chainID, or "" if there is
// none.
func proxyVersion(code []byte, chainID uint64) string {
	proxy := safeProxyCodeHashes[ethrpc.Hash(keccak256.Hash(code))]
	if !slices.Contains(proxy.networks, chainID) {
		return ""
	}
	return proxy.version
}

// singletons are the Safe and SafeL2 singletons of the Safe versions that the
// Charter lists in § 2.1, by version, with "+L2" for SafeL2. They are listed in
// the safe-deployments repository at
// https://github.com/safe-global/safe-deployments, which lists each of them on
// every network that the Charter covers. They are deterministic deployments, so
// an address has either the singleton's code or none at all.
var singletons = map[ethrpc.Address]string{
	ethrpc.MustParseAddress("0xd9Db270c1B5E3Bd161E8c8503c55cEABeE709552"): "1.3.0",
	ethrpc.MustParseAddress("0x69f4D1788e39c87893C980c06EdF4b7f686e2938"): "1.3.0",
	ethrpc.MustParseAddress("0x3E5c63644E683549055b9Be8653de26E0B4CD36E"): "1.3.0+L2",
	ethrpc.MustParseAddress("0xfb1bffC9d739B8D520DaF37dF666da4C687191EA"): "1.3.0+L2",
	ethrpc.MustParseAddress("0x41675C099F32341bf84BFc5382aF534df5C7461a"): "1.4.1",
	ethrpc.MustParseAddress("0x29fcB43b46531BcA003ddC8FCB67FFE91900C762"): "1.4.1+L2",
	ethrpc.MustParseAddress("0xFf51A5898e281Db6DfC7855790607438dF2ca44b"): "1.5.0",
	ethrpc.MustParseAddress("0xEdd160fEBBD92E350D4D398fb636302fccd67C7e"): "1.5.0+L2",
}

// unsupportedSafe finds a call by an account that isn't a Safe insecure, under
// R-4.0 of the Charter
// (bafkreigg6ajqdb52kb644wcxegurt7r6jlclru4mjy3lwxkbh6avm3vgre): a transaction
// is insecure if its Safe address isn't a Safe (§ 2.1) on its network at
// proposal time, and such a transaction isn't out of scope (§ 3.9).
//
// An account is a Safe if, at the block before the proposal, it has the code of
// a SafeProxy in safeProxyCodeHashes for its network, of proxy version 1.0.0,
// 1.1.1, 1.3.0, 1.4.1, or 1.5.0, and its singleton, in storage slot 0, is one
// of singletons, of version 1.3.0, 1.4.1, or 1.5.0. The proxy doesn't need to
// have been deployed by an official proxy factory. The proxy delegates calls to
// the address in the low 20 bytes of the slot, so the others are ignored. It
// runs after offNetwork, so the Safe is on a network that the Charter covers.
var unsupportedSafe = check{
	verdict:     Insecure,
	rule:        "R-4.0",
	description: "account that isn't a Safe of a version that the Charter covers",
	fn: func(ctx context.Context, env *env, safe *safeID, _ call) (bool, error) {
		version, err := safeVersion(ctx, env, safe)
		return version == "", err
	},
}

// safeAt identifies an account on a chain at a block.
type safeAt struct {
	address ethrpc.Address
	chainID uint64
	block   uint64
}

// safeVersions caches safeVersion by safeAt, since checks run on every call of
// a transaction, and a chain's state at a block doesn't change.
var safeVersions sync.Map

// safeVersion returns the version of the Safe, as singletons has it, at env's
// block, or "" if the account isn't a Safe of a version that § 2.1 lists. The
// Safe must be on a network that the Charter covers, as it is for every check
// after offNetwork.
func safeVersion(ctx context.Context, env *env, safe *safeID) (string, error) {
	key := safeAt{address: safe.address, chainID: safe.chainID.Uint64(), block: env.block}
	if version, ok := safeVersions.Load(key); ok {
		return version.(string), nil
	}
	client, err := env.dial(ctx, key.chainID)
	if err != nil {
		return "", err
	}
	// The account is read with eth_getCode and eth_getStorageAt rather than with a
	// single eth_getProof, since fewer nodes serve eth_getProof at older blocks.
	block := ethrpc.BlockNumber(env.block)
	code, err := client.GetCode(ctx, safe.address, block)
	if err != nil {
		return "", err
	}
	var version string
	if proxyVersion(code, key.chainID) != "" {
		slot0, err := client.GetStorageAt(ctx, safe.address, ethrpc.Hash{}, block)
		if err != nil {
			return "", err
		}
		version = singletons[ethrpc.Address(slot0[12:])]
	}
	safeVersions.Store(key, version)
	return version, nil
}
