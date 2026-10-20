package ethrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/safe-research/safenet-arbitration-bot/internal/httpretry"
)

// RPCListURL is Chainlist's list of public RPC endpoints, a JSON array of
// chains, each with a chain ID and a list of RPC URLs.
const RPCListURL = "https://chainlist.org/rpcs.json"

// maxRPCListSize bounds the size of the downloaded RPC list, which covers
// thousands of chains (about 2.3 MB as of September 2026).
const maxRPCListSize = 32 << 20

// rpcList is an RPC list that is downloaded once, on first use, and shared by
// every client in the process.
type rpcList struct {
	url  string
	http *http.Client

	mu sync.Mutex
	// rpcs maps chain IDs to their RPC URLs, or is nil if the list hasn't been
	// downloaded yet. A failed download leaves it nil, so the next call retries.
	rpcs map[uint64][]string
}

var defaultRPCs = &rpcList{url: RPCListURL, http: httpretry.Client}

// DefaultRPCs returns the public HTTP(S) RPC URLs from RPCListURL for the chain
// with the given ID. The list is downloaded at most once per process.
func DefaultRPCs(ctx context.Context, chainID uint64) ([]string, error) {
	rpcs, err := defaultRPCs.get(ctx)
	if err != nil {
		return nil, err
	}
	urls := rpcs[chainID]
	if len(urls) == 0 {
		return nil, fmt.Errorf("RPC list has no RPC URLs for chain %d", chainID)
	}
	return urls, nil
}

// get returns the RPC URLs by chain ID, downloading the list if needed.
func (l *rpcList) get(ctx context.Context) (map[uint64][]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rpcs == nil {
		rpcs, err := l.download(ctx)
		if err != nil {
			return nil, err
		}
		l.rpcs = rpcs
	}
	return l.rpcs, nil
}

// download fetches and parses the RPC list. It keeps only HTTP(S) URLs,
// skipping WebSocket URLs and templates that need an API key filled in, such as
// "https://mainnet.infura.io/v3/${INFURA_API_KEY}".
func (l *rpcList) download(ctx context.Context) (map[uint64][]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching RPC list: %w", err)
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching RPC list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetching RPC list: HTTP %s: %s", resp.Status, bytes.TrimSpace(snippet))
	}

	var chains []struct {
		ChainID uint64 `json:"chainId"`
		RPC     []struct {
			URL string `json:"url"`
		} `json:"rpc"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRPCListSize)).Decode(&chains); err != nil {
		return nil, fmt.Errorf("decoding RPC list: %w", err)
	}
	rpcs := make(map[uint64][]string)
	for _, chain := range chains {
		for _, rpc := range chain.RPC {
			u, err := url.Parse(rpc.URL)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Contains(rpc.URL, "${") {
				continue
			}
			rpcs[chain.ChainID] = append(rpcs[chain.ChainID], rpc.URL)
		}
	}
	if len(rpcs) == 0 {
		return nil, errors.New("RPC list has no valid RPC URLs")
	}
	return rpcs, nil
}
