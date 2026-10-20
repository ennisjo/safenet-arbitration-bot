package ipfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/safe-research/safenet-arbitration-bot/internal/httpretry"
)

// GatewayListURL is the list of public HTTP gateways maintained by the IPFS
// public gateway checker, a JSON array of gateway base URLs.
const GatewayListURL = "https://raw.githubusercontent.com/ipfs/public-gateway-checker/refs/heads/main/gateways.json"

// maxGatewayListSize bounds the size of the downloaded gateway list.
const maxGatewayListSize = 1 << 20

// gatewayList is a gateway list that is downloaded once, on first use, and
// shared by every client in the process.
type gatewayList struct {
	url  string
	http *http.Client

	mu sync.Mutex
	// gateways is the downloaded list, or nil if it hasn't been downloaded yet. A
	// failed download leaves it nil, so the next call retries.
	gateways []string
}

var defaultGateways = &gatewayList{url: GatewayListURL, http: httpretry.Client}

// DefaultGateways returns the public gateways from GatewayListURL. The list is
// downloaded at most once per process.
func DefaultGateways(ctx context.Context) ([]string, error) {
	return defaultGateways.get(ctx)
}

// get returns the gateway list, downloading it if needed. The returned list is
// never empty.
func (l *gatewayList) get(ctx context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gateways == nil {
		gateways, err := l.download(ctx)
		if err != nil {
			return nil, err
		}
		l.gateways = gateways
	}
	return l.gateways, nil
}

// download fetches and parses the gateway list, skipping any entry that isn't
// an HTTP(S) base URL.
func (l *gatewayList) download(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching gateway list: %w", err)
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching gateway list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetching gateway list: HTTP %s: %s", resp.Status, bytes.TrimSpace(snippet))
	}

	var entries []string
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGatewayListSize)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decoding gateway list: %w", err)
	}
	var gateways []string
	for _, entry := range entries {
		if u, err := url.Parse(entry); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			gateways = append(gateways, entry)
		}
	}
	if len(gateways) == 0 {
		return nil, errors.New("gateway list has no valid gateways")
	}
	return gateways, nil
}
