package ipfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/httpretry"
	"github.com/safe-research/safenet-arbitration-bot/internal/xdg"
)

// MaxContentSize is the largest content that Fetch accepts. Supported CIDs
// address a single raw block, and IPFS limits blocks to 2 MiB.
const MaxContentSize = 2 << 20

// Client fetches content from an IPFS HTTP gateway, caching it on disk.
type Client struct {
	http *http.Client
	// cache is the directory holding cached content, one file per CID. An empty
	// string disables caching.
	cache string

	mu sync.Mutex
	// url is the gateway base URL. An empty string means that the client hasn't
	// picked one of the default gateways yet.
	url string
}

// gatewayTimeout bounds how long a client waits for the default gateways to
// serve content while picking one, so that unresponsive gateways don't stall
// it.
var gatewayTimeout = 30 * time.Second

// NewClient returns a client for the HTTP gateway at the base URL url, such as
// "https://ipfs.filebase.io". If url is empty, the client uses the default
// gateways (see DefaultGateways): the first time it needs to download content,
// it requests the content from all of them at once, since public gateways are
// often slow, and keeps using the first gateway that serves it. Content is
// cached in $XDG_CACHE_HOME/arbot/ipfs.
func NewClient(url string) *Client {
	var cache string
	if dir := xdg.CacheHome(); dir != "" {
		cache = filepath.Join(dir, "arbot", "ipfs")
	}
	return &Client{http: httpretry.Client, cache: cache, url: url}
}

// Fetch returns the content addressed by cid. Content is immutable, so it is
// served from the cache when present, and otherwise downloaded from
// {gateway}/ipfs/{cid} and cached. Neither gateways nor the cache are trusted:
// it returns an error unless the content matches cid.
func (c *Client) Fetch(ctx context.Context, cid CID) ([]byte, error) {
	if content, ok := c.cached(cid); ok {
		return content, nil
	}
	content, err := c.download(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", cid, err)
	}
	// The cache only saves future downloads, so failing to write it does not fail
	// the fetch.
	_ = c.store(cid, content)
	return content, nil
}

// cached returns the cached content for cid, if the cache holds a valid copy. A
// missing or corrupted entry is a cache miss and gets overwritten by the next
// store.
func (c *Client) cached(cid CID) ([]byte, bool) {
	if c.cache == "" {
		return nil, false
	}
	content, err := os.ReadFile(filepath.Join(c.cache, cid.String()))
	if err != nil || ComputeCID(content) != cid {
		return nil, false
	}
	return content, true
}

// store writes content to the cache. It writes to a temporary file and renames
// it into place, so concurrent runs never read a partial entry.
func (c *Client) store(cid CID, content []byte) error {
	if c.cache == "" {
		return nil
	}
	if err := os.MkdirAll(c.cache, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(c.cache, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(c.cache, cid.String()))
}

// download fetches the content for cid from the client's gateway, picking one
// of the default gateways if it has none yet.
func (c *Client) download(ctx context.Context, cid CID) ([]byte, error) {
	c.mu.Lock()
	gateway := c.url
	c.mu.Unlock()
	if gateway != "" {
		return c.downloadFrom(ctx, gateway, cid)
	}

	gateways, err := DefaultGateways(ctx)
	if err != nil {
		return nil, err
	}

	// Returning cancels the requests still in flight. The channel has room for
	// every result, so their goroutines never block.
	raceCtx, cancel := context.WithTimeout(ctx, gatewayTimeout)
	defer cancel()
	type result struct {
		gateway string
		content []byte
		err     error
	}
	results := make(chan result, len(gateways))
	for _, gateway := range gateways {
		go func() {
			content, err := c.downloadFrom(raceCtx, gateway, cid)
			results <- result{gateway, content, err}
		}()
	}

	var errs []error
	for range gateways {
		r := <-results
		if r.err == nil {
			c.mu.Lock()
			c.url = r.gateway
			c.mu.Unlock()
			return r.content, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", r.gateway, r.err))
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("no default gateway serves the content: %w", errors.Join(errs...))
}

// downloadFrom fetches the content for cid from the gateway at the base URL
// gateway, and verifies it.
func (c *Client) downloadFrom(ctx context.Context, gateway string, cid CID) ([]byte, error) {
	u, err := url.JoinPath(gateway, "ipfs", cid.String())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, bytes.TrimSpace(snippet))
	}

	// Read one byte past the limit to tell content that is exactly at the limit
	// apart from content that exceeds it.
	content, err := io.ReadAll(io.LimitReader(resp.Body, MaxContentSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxContentSize {
		return nil, fmt.Errorf("content exceeds %d bytes", MaxContentSize)
	}
	if got := ComputeCID(content); got != cid {
		return nil, fmt.Errorf("gateway returned content with CID %s", got)
	}
	return content, nil
}
