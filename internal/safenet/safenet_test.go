package safenet

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

var (
	update = flag.Bool("update", false, "update the golden files in testdata")
	record = flag.Bool("record", false, "record the block headers that testdata/headers.json lacks from each chain's default RPCs")
)

// fixture is a snapshot of a Gnosis Chain deployment as of a block, which it
// serves as the latest block: the call results, logs, and block headers that
// Pending and Request read. The deployment is fixtureOracle and
// fixtureConsensus, an earlier default deployment, which has requests in every
// state.
//
// testdata/gnosis.json was recorded through a JSON-RPC proxy in front of
// https://rpc.gnosischain.com, while running `arbot pending` and `arbot info`
// for the requests in TestRequest, and for an unknown request ID, at the
// fixture's block. The calls for the fee token were added later, with cast.
//
// testdata/headers.json holds, by chain ID, the block headers that Request
// reads to find the blocks before each proposal: the proposal blocks' parents
// on Gnosis Chain, and the latest block and the blocks that SearchBlock probes
// on the other chains. Run TestRequest with -record to fetch the headers that
// it lacks, such as after changing SearchBlock.
// The SentinelOracle and Consensus of the deployment in testdata/gnosis.json.
var (
	fixtureOracle    = ethrpc.MustParseAddress("0x544F12bAd6FF72564abBc7eA6494A2a4BdD0DDD0")
	fixtureConsensus = ethrpc.MustParseAddress("0x98810887769db19A0Df9bf2f44E4998856fcb390")
)

type fixture struct {
	Block  ethrpc.BlockNumber `json:"block"`
	Calls  []fixtureCall      `json:"calls"`
	Logs   []ethrpc.Log       `json:"logs"`
	Blocks []ethrpc.Block     `json:"blocks"`

	mu      sync.Mutex
	headers map[uint64]*headers
	// live are clients for the chains' default RPCs, for recording headers.
	live map[uint64]*ethrpc.Client
}

// headers are the recorded block headers of a chain.
type headers struct {
	Latest ethrpc.BlockNumber `json:"latest,omitempty"`
	Blocks []ethrpc.Block     `json:"blocks"`
}

// fixtureCall is an eth_call and its result, or its error if it reverted.
type fixtureCall struct {
	To     ethrpc.Address `json:"to"`
	Data   ethrpc.Bytes   `json:"data"`
	Result ethrpc.Bytes   `json:"result"`
	Error  *ethrpc.Error  `json:"error"`
}

func loadFixture(t *testing.T) *fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/gnosis.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	if data, err = os.ReadFile("testdata/headers.json"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &f.headers); err != nil {
		t.Fatalf("decoding headers: %v", err)
	}
	f.live = make(map[uint64]*ethrpc.Client)
	return &f
}

// open returns a Safenet that reads the fixture's deployment from a node serving
// the fixture, and the other chains from nodes serving the recorded headers.
func (f *fixture) open(t *testing.T) *Safenet {
	t.Helper()
	return New(f.dial(t), fixtureOracle, fixtureConsensus)
}

// dial returns a Dialer for a node that serves the fixture on Gnosis Chain, and
// for nodes that serve the recorded headers of the other chains.
func (f *fixture) dial(t *testing.T) ethrpc.Dialer {
	t.Helper()
	gnosis := f.serve(t)
	return func(ctx context.Context, chainID uint64) (*ethrpc.Client, error) {
		if chainID == ethrpc.Gnosis {
			return gnosis, nil
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     uint64            `json:"id"`
				Method string            `json:"method"`
				Params []json.RawMessage `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var result any
			var err error
			switch req.Method {
			case "eth_chainId":
				result = ethrpc.Quantity(chainID)
			case "eth_blockNumber":
				result, err = f.latest(ctx, chainID)
			case "eth_getBlockByNumber":
				var number ethrpc.BlockNumber
				var full bool
				if err = decodeParams(req.Params, &number, &full); err == nil {
					result, err = f.header(ctx, chainID, number)
				}
			default:
				err = fmt.Errorf("unexpected method")
			}
			if err != nil {
				t.Errorf("chain %d: %s: %v", chainID, req.Method, err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}))
		t.Cleanup(server.Close)
		return ethrpc.NewClient(ctx, chainID, server.URL)
	}
}

// chain returns the recorded headers of the chain with the given ID. It must be
// called with f.mu held.
func (f *fixture) chain(chainID uint64) *headers {
	if f.headers == nil {
		f.headers = make(map[uint64]*headers)
	}
	if f.headers[chainID] == nil {
		f.headers[chainID] = &headers{Blocks: []ethrpc.Block{}}
	}
	return f.headers[chainID]
}

// latest returns the recorded latest block of the chain with the given ID. With
// -record, it records the chain's latest block if it has none.
func (f *fixture) latest(ctx context.Context, chainID uint64) (ethrpc.BlockNumber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.chain(chainID)
	if h.Latest == 0 && *record {
		eth, err := f.liveClient(ctx, chainID)
		if err != nil {
			return 0, err
		}
		if h.Latest, err = eth.BlockNumber(ctx); err != nil {
			return 0, err
		}
	}
	if h.Latest == 0 {
		return 0, fmt.Errorf("no recorded latest block; run TestRequest with -record")
	}
	return h.Latest, nil
}

// header returns the recorded header of block number on the chain with the
// given ID. With -record, it records the header if it is missing.
func (f *fixture) header(ctx context.Context, chainID uint64, number ethrpc.BlockNumber) (ethrpc.Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.chain(chainID)
	for _, block := range h.Blocks {
		if block.Number == number {
			return block, nil
		}
	}
	if !*record {
		return ethrpc.Block{}, fmt.Errorf("no recorded block %d; run TestRequest with -record", number)
	}
	eth, err := f.liveClient(ctx, chainID)
	if err != nil {
		return ethrpc.Block{}, err
	}
	block, err := eth.GetBlockByNumber(ctx, number)
	if err != nil {
		return ethrpc.Block{}, err
	}
	h.Blocks = append(h.Blocks, block)
	return block, nil
}

// liveClient returns a client for a default RPC of the chain with the given ID.
// It must be called with f.mu held.
func (f *fixture) liveClient(ctx context.Context, chainID uint64) (*ethrpc.Client, error) {
	if eth := f.live[chainID]; eth != nil {
		return eth, nil
	}
	eth, err := ethrpc.NewClient(ctx, chainID, "")
	if err != nil {
		return nil, err
	}
	f.live[chainID] = eth
	return eth, nil
}

// saveHeaders writes the recorded headers to testdata/headers.json, formatted
// as jq formats it.
func (f *fixture) saveHeaders(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.headers {
		slices.SortFunc(h.Blocks, func(a, b ethrpc.Block) int { return cmp.Compare(a.Number, b.Number) })
	}
	data, err := json.MarshalIndent(f.headers, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/headers.json", append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// serve returns a client for a node that serves the fixture. A request that the
// fixture has no answer for fails the test.
func (f *fixture) serve(t *testing.T) *ethrpc.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, rpcErr, err := f.handle(req.Method, req.Params)
		if err != nil {
			// The handler runs on the server's goroutine, where t.Fatal must not be called,
			// so it only reports the error.
			t.Errorf("%s: %v", req.Method, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		if rpcErr != nil {
			response = map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": rpcErr}
		}
		json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)

	eth, err := ethrpc.NewClient(t.Context(), ethrpc.Gnosis, server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return eth
}

// handle answers a JSON-RPC request from the fixture. It returns an error for
// requests that the fixture has no answer for, including ones for blocks after
// the fixture's block.
func (f *fixture) handle(method string, params []json.RawMessage) (any, *ethrpc.Error, error) {
	switch method {
	case "eth_chainId":
		return "0x64", nil, nil
	case "eth_blockNumber":
		return f.Block, nil, nil
	case "eth_call":
		var call ethrpc.CallRequest
		var block ethrpc.BlockNumber
		if err := decodeParams(params, &call, &block); err != nil {
			return nil, nil, err
		}
		if block != f.Block {
			return nil, nil, fmt.Errorf("call at block %d, but the fixture is at block %d", block, f.Block)
		}
		for _, c := range f.Calls {
			if c.To == call.To && bytes.Equal(c.Data, call.Data) {
				if c.Error != nil {
					return nil, c.Error, nil
				}
				return c.Result, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("no recorded call to %s with data %s", call.To, call.Data)
	case "eth_getLogs":
		var filter ethrpc.LogFilter
		if err := decodeParams(params, &filter); err != nil {
			return nil, nil, err
		}
		if filter.ToBlock > f.Block {
			return nil, nil, fmt.Errorf("logs up to block %d, but the fixture is at block %d", filter.ToBlock, f.Block)
		}
		logs := []ethrpc.Log{}
		for _, log := range f.Logs {
			if matches(filter, log) {
				logs = append(logs, log)
			}
		}
		return logs, nil, nil
	case "eth_getBlockByNumber":
		var number ethrpc.BlockNumber
		var full bool
		if err := decodeParams(params, &number, &full); err != nil {
			return nil, nil, err
		}
		for _, block := range f.Blocks {
			if block.Number == number {
				return block, nil, nil
			}
		}
		block, err := f.header(context.Background(), ethrpc.Gnosis, number)
		if err != nil {
			return nil, nil, err
		}
		return block, nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected method")
}

func decodeParams(params []json.RawMessage, values ...any) error {
	if len(params) != len(values) {
		return fmt.Errorf("got %d parameters, want %d", len(params), len(values))
	}
	for i, value := range values {
		if err := json.Unmarshal(params[i], value); err != nil {
			return fmt.Errorf("parameter %d: %w", i, err)
		}
	}
	return nil
}

// matches reports whether filter selects log.
func matches(filter ethrpc.LogFilter, log ethrpc.Log) bool {
	if log.BlockNumber < filter.FromBlock || log.BlockNumber > filter.ToBlock {
		return false
	}
	if len(filter.Addresses) > 0 && !slices.Contains(filter.Addresses, log.Address) {
		return false
	}
	for i, topics := range filter.Topics {
		if topics != nil && (i >= len(log.Topics) || !slices.Contains(topics, log.Topics[i])) {
			return false
		}
	}
	return true
}

// log returns the fixture's log of event whose first indexed topic is topic.
func (f *fixture) log(t *testing.T, event, topic ethrpc.Hash) *ethrpc.Log {
	t.Helper()
	for i, log := range f.Logs {
		if log.Topics[0] == event && log.Topics[1] == topic {
			return &f.Logs[i]
		}
	}
	t.Fatalf("fixture has no log of event %s with topic %s", event, topic)
	return nil
}

// removeLogs removes the fixture's logs of event whose first indexed topic is
// topic.
func (f *fixture) removeLogs(t *testing.T, event, topic ethrpc.Hash) {
	t.Helper()
	n := len(f.Logs)
	f.Logs = slices.DeleteFunc(f.Logs, func(log ethrpc.Log) bool {
		return log.Topics[0] == event && log.Topics[1] == topic
	})
	if len(f.Logs) == n {
		t.Fatalf("fixture has no log of event %s with topic %s", event, topic)
	}
}

// Requests in the fixture.
var (
	frozenID              = mustParseHash("0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb")
	frozenSafeTxHash      = mustParseHash("0xea6f04a866f107949f795b60dbcbe9563ab67ee232bbb14c7b85644dc9fce221")
	insecureID            = mustParseHash("0x3c25a142c4a7e1eaac635f2f271acc4d7d9ed9109c54321b25556ca68e5dfa6b")
	secureID              = mustParseHash("0x68f9225d10c8f8bf8b8a13821d7bcfe70a4f65c63121d154ef13c01bcc17c36c")
	outOfScopeID          = mustParseHash("0xdcc1fd09f0f6e226179f2bbaa852d45f1df3dbf5f18916b100165511e8888eed")
	arbitrationTimedOutID = mustParseHash("0x9638bb644c8626d39168db94daca90cfb2908e174c5fb6284adfbdd547812945")
	unanimousID           = mustParseHash("0x6873954d892a95127ddca0f8972902134b0d509e1d40190854b9ba3b1e7e8fda")
	requestTimedOutID     = mustParseHash("0xbf8a51ff22a488e078e455e1a7579f5112b14b54cc4ca1cb7bed6efa6e8ede38")
)

func TestPending(t *testing.T) {
	f := loadFixture(t)
	disputes, err := f.open(t).Pending(t.Context())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}

	if len(disputes) != 38 {
		t.Errorf("Pending: got %d disputes, want 38", len(disputes))
	}
	if want := (Dispute{RequestID: frozenID, FrozenBlock: 48385788, Deadline: 48436188}); len(disputes) > 0 && disputes[0] != want {
		t.Errorf("Pending: got first dispute %+v, want %+v", disputes[0], want)
	}
	if !slices.IsSortedFunc(disputes, func(a, b Dispute) int { return cmp.Compare(a.Deadline, b.Deadline) }) {
		t.Error("Pending: disputes are not ordered by deadline")
	}
	for _, d := range disputes {
		if d.Deadline < uint64(f.Block) {
			t.Errorf("Pending: dispute %s has deadline %d, before block %d", d.RequestID, d.Deadline, f.Block)
		}
	}

	// These requests were frozen and then settled within the scanned blocks.
	for _, id := range []ethrpc.Hash{insecureID, secureID, outOfScopeID} {
		f.log(t, disputeTriggeredEvent, id)
		if slices.ContainsFunc(disputes, func(d Dispute) bool { return d.RequestID == id }) {
			t.Errorf("Pending: got settled dispute %s", id)
		}
	}
}

func TestPendingNone(t *testing.T) {
	f := loadFixture(t)
	f.Logs = nil
	disputes, err := f.open(t).Pending(t.Context())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if disputes == nil || len(disputes) != 0 {
		t.Errorf("Pending: got %#v, want an empty slice", disputes)
	}
}

func TestRequest(t *testing.T) {
	tests := []struct {
		name  string
		id    ethrpc.Hash
		state State
		// outcome is empty for a request without an arbitration.
		outcome Outcome
		votes   int
	}{
		{"frozen", frozenID, StateFrozen, OutcomePending, 2},
		{"insecure", insecureID, StateResolvedDenied, OutcomeInsecure, 2},
		{"secure", secureID, StateResolvedApproved, OutcomeSecure, 3},
		{"out of scope", outOfScopeID, StateTimedOut, OutcomeOutOfScope, 2},
		{"arbitration timed out", arbitrationTimedOutID, StateTimedOut, OutcomeTimedOut, 3},
		{"unanimous", unanimousID, StateResolvedApproved, "", 1},
		{"no reveals", requestTimedOutID, StateTimedOut, "", 1},
	}

	f := loadFixture(t)
	if *record {
		t.Cleanup(func() { f.saveHeaders(t) })
	}
	sn := f.open(t)
	var requests []*Request
	for _, test := range tests {
		request, err := sn.Request(t.Context(), test.id)
		if err != nil {
			t.Errorf("Request(%s): %v", test.name, err)
			continue
		}
		requests = append(requests, request)

		if request.State != test.state {
			t.Errorf("Request(%s): got state %s, want %s", test.name, request.State, test.state)
		}
		var outcome Outcome
		if request.Arbitration != nil {
			outcome = request.Arbitration.Outcome
		}
		if outcome != test.outcome {
			t.Errorf("Request(%s): got outcome %q, want %q", test.name, outcome, test.outcome)
		}
		if len(request.Votes) != test.votes {
			t.Errorf("Request(%s): got %d votes, want %d", test.name, len(request.Votes), test.votes)
		}
	}

	// Encode the requests as jq formats them, which `just check-testdata` checks.
	var got bytes.Buffer
	encoder := json.NewEncoder(&got)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(requests); err != nil {
		t.Fatal(err)
	}
	golden := "testdata/requests.golden.json"
	if *update {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("Request: JSON differs from %s; run the tests with -update to see the difference", golden)
	}
}

// TestRequestJSONRoundTrip checks that requests decode from the JSON that they
// encode to, as `arbot classify -request-file` reads them.
func TestRequestJSONRoundTrip(t *testing.T) {
	want, err := os.ReadFile("testdata/requests.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var requests []*Request
	decoder := json.NewDecoder(bytes.NewReader(want))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&requests); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	encoder := json.NewEncoder(&got)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(requests); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("requests encode to different JSON after decoding:\n%s", got.Bytes())
	}
}

func TestEnumUnmarshalText(t *testing.T) {
	var state State
	if err := state.UnmarshalText([]byte("FROZEN")); err != nil || state != StateFrozen {
		t.Errorf("State.UnmarshalText(FROZEN) = %v, %v; want %v", state, err, StateFrozen)
	}
	for _, text := range []string{"frozen", "UNKNOWN(9)", ""} {
		if err := state.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("State.UnmarshalText(%q): expected an error", text)
		}
	}
}

// TestRequestBlocks checks the blocks before proposals of Safe transactions on
// another chain, on Gnosis Chain, and on Ethereum Mainnet. The Arbitrum and
// Mainnet blocks were checked with cast: they are the last ones before the
// proposal time, and the next Arbitrum block is at the proposal time.
func TestRequestBlocks(t *testing.T) {
	tests := []struct {
		name           string
		id             ethrpc.Hash
		ethereum, safe uint64
	}{
		{"Arbitrum", frozenID, 26034967, 507875025},
		// The Gnosis Chain block before the proposal block.
		{"Gnosis Chain", insecureID, 26047854, 48415710},
		{"Ethereum Mainnet", secureID, 26047997, 26047997},
	}
	f := loadFixture(t)
	sn := f.open(t)
	for _, test := range tests {
		request, err := sn.Request(t.Context(), test.id)
		if err != nil {
			t.Errorf("Request(%s): %v", test.name, err)
			continue
		}
		p := request.Proposal
		if p.EthereumBlock != test.ethereum || p.SafeBlock != test.safe {
			t.Errorf("Request(%s): got Ethereum block %d and Safe chain block %d before the proposal, want %d and %d",
				test.name, p.EthereumBlock, p.SafeBlock, test.ethereum, test.safe)
		}
	}
}

func TestRequestDialError(t *testing.T) {
	f := loadFixture(t)
	fixtureDial := f.dial(t)
	// Only Gnosis Chain can be reached.
	dial := func(ctx context.Context, chainID uint64) (*ethrpc.Client, error) {
		if chainID == ethrpc.Gnosis {
			return fixtureDial(ctx, chainID)
		}
		return nil, errors.New("no RPC")
	}
	sn := New(dial, fixtureOracle, fixtureConsensus)
	wantError(t, sn, frozenID, "connecting to chain 1: no RPC")
	wantError(t, sn, frozenID, "connecting to chain 42161: no RPC")

	// Nothing can be reached.
	sn = New(func(context.Context, uint64) (*ethrpc.Client, error) { return nil, errors.New("no RPC") }, fixtureOracle, fixtureConsensus)
	wantError(t, sn, frozenID, "connecting to Gnosis Chain: no RPC")
	if _, err := sn.Pending(t.Context()); err == nil || !strings.Contains(err.Error(), "connecting to Gnosis Chain: no RPC") {
		t.Errorf("Pending: got error %v, want one saying that Gnosis Chain can't be reached", err)
	}
}

func TestRequestNotFound(t *testing.T) {
	f := loadFixture(t)
	id := ethrpc.Hash{31: 1}
	if _, err := f.open(t).Request(t.Context(), id); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("Request: got error %v, want %v", err, ErrRequestNotFound)
	}
}

func TestRequestWrongConsensus(t *testing.T) {
	f := loadFixture(t)
	sn := New(f.dial(t), fixtureOracle, ethrpc.Address{19: 1})
	wantError(t, sn, frozenID, "has PROPOSER")
}

func TestRequestProposalMismatch(t *testing.T) {
	t.Run("request ID", func(t *testing.T) {
		f := loadFixture(t)
		// Changing the epoch changes the request ID that the proposal hashes to.
		log := f.log(t, transactionProposedEvent, frozenSafeTxHash)
		log.Data[31] ^= 1
		wantError(t, f.open(t), frozenID, "no proposal in block 48385778 hashes to the request ID")
	})
	t.Run("Safe transaction hash", func(t *testing.T) {
		f := loadFixture(t)
		// Changing the Safe transaction's nonce, its last field, changes its hash but
		// not the logged hash, which the request ID commits to.
		log := f.log(t, transactionProposedEvent, frozenSafeTxHash)
		tx := binary.BigEndian.Uint64(log.Data[2*32+24 : 3*32])
		log.Data[tx+12*32-1] ^= 1
		wantError(t, f.open(t), frozenID, "Safe transaction hashes to")
	})
}

func TestRequestVoteMismatch(t *testing.T) {
	f := loadFixture(t)
	f.removeLogs(t, revealedEvent, frozenID)
	wantError(t, f.open(t), frozenID, "logs have 2 commits, 0 reveals")
}

func TestRequestArbitrationMismatch(t *testing.T) {
	t.Run("not frozen", func(t *testing.T) {
		f := loadFixture(t)
		f.removeLogs(t, disputeTriggeredEvent, frozenID)
		wantError(t, f.open(t), frozenID, "no DisputeTriggered log")
	})
	t.Run("not settled", func(t *testing.T) {
		f := loadFixture(t)
		f.removeLogs(t, disputeResolvedEvent, insecureID)
		wantError(t, f.open(t), insecureID, "is RESOLVED_DENIED, but its arbitration logs have outcome pending")
	})
	t.Run("wrong outcome", func(t *testing.T) {
		f := loadFixture(t)
		// Change the ruling of the RESOLVED_DENIED request from insecure to secure.
		log := f.log(t, disputeResolvedEvent, insecureID)
		log.Data[31] = byte(StateResolvedApproved)
		wantError(t, f.open(t), insecureID, "is RESOLVED_DENIED, but its arbitration logs have outcome secure")
	})
}

// wantError checks that Request fails for id with an error containing want.
func wantError(t *testing.T, sn *Safenet, id ethrpc.Hash, want string) {
	t.Helper()
	_, err := sn.Request(t.Context(), id)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Request: got error %v, want one containing %q", err, want)
	}
}
