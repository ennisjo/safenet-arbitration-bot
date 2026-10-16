package e2e

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/safe-research/safenet-arbitration-bot/internal/ens"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/ipfs"
)

// charterResolver is the ENS resolver that InstallCharter installs.
var charterResolver = ethrpc.Address{0: 0xe5, 19: 0x5e}

// InstallCharter makes content the Charter on the Mainnet node, and returns its
// CID: the node's ENS registry gives every name a resolver whose content hash
// is the CID. It must be called before Arbot mines the Mainnet blocks before
// the proposals, so that they have it.
func (n *Testnet) InstallCharter(content []byte) ipfs.CID {
	n.tb.Helper()
	var resolver [32]byte
	copy(resolver[12:], charterResolver[:])
	// The content hash is the IPFS namespace, then the CIDv1 of the raw content
	// with its SHA2-256 digest, ABI-encoded as bytes.
	digest := sha256.Sum256(content)
	contenthash := append([]byte{0xe3, 0x01, 0x01, 0x55, 0x12, 0x20}, digest[:]...)
	var encoded [128]byte
	encoded[31], encoded[63] = 0x20, byte(len(contenthash))
	copy(encoded[64:], contenthash)
	n.Mainnet.rpc(nil, "anvil_setCode", ens.Registry, returning(resolver[:]))
	n.Mainnet.rpc(nil, "anvil_setCode", charterResolver, returning(encoded[:]))
	return ipfs.ComputeCID(content)
}

// returning returns the code of a contract that returns data from any call.
func returning(data []byte) ethrpc.Bytes {
	// CODECOPY(0, 14, size), then RETURN(0, size), followed by data at offset 14.
	size := []byte{byte(len(data) >> 8), byte(len(data))}
	code := []byte{0x61, size[0], size[1], 0x60, 14, 0x60, 0x00, 0x39, 0x61, size[0], size[1], 0x60, 0x00, 0xf3}
	return append(code, data...)
}

// gateway is an IPFS gateway that serves the content of a test.
type gateway struct {
	*httptest.Server
	mu      sync.Mutex
	content map[string][]byte
}

// ServeIPFS serves content from the IPFS gateway that Arbot configures arbot to
// use, starting it if needed, and returns its CID.
func (n *Testnet) ServeIPFS(content []byte) ipfs.CID {
	n.tb.Helper()
	if n.gateway == nil {
		g := &gateway{content: make(map[string][]byte)}
		g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			g.mu.Lock()
			content, ok := g.content[strings.TrimPrefix(r.URL.Path, "/ipfs/")]
			g.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(content)
		}))
		n.tb.Cleanup(g.Close)
		n.gateway = g
	}
	cid := ipfs.ComputeCID(content)
	n.gateway.mu.Lock()
	defer n.gateway.mu.Unlock()
	n.gateway.content[cid.String()] = content
	return cid
}
