package checks

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

func TestClassification(t *testing.T) {
	tests := []struct {
		c    Classification
		text string
		json string
	}{
		{Classification{}, "unclassified", `{"verdict":null}`},
		{Classification{Verdict: Secure}, "secure", `{"verdict":"secure"}`},
		{
			Classification{Verdict: Insecure, Rule: "R-4.1", Description: "adds an owner"},
			"insecure  R-4.1  adds an owner",
			`{"verdict":"insecure","rule":"R-4.1","description":"adds an owner"}`,
		},
	}
	for _, test := range tests {
		if got := test.c.String(); got != test.text {
			t.Errorf("%+v.String() = %q, want %q", test.c, got, test.text)
		}
		got, err := json.Marshal(test.c)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != test.json {
			t.Errorf("json.Marshal(%+v) = %s, want %s", test.c, got, test.json)
		}
	}
}

// TestChecks checks that every check has a verdict and a description, and a
// rule if and only if it is for insecure requests, and that the checks are
// grouped by verdict in verdictOrder.
func TestChecks(t *testing.T) {
	if !slices.IsSortedFunc(checks, func(a, b check) int {
		return cmp.Compare(slices.Index(verdictOrder, a.verdict), slices.Index(verdictOrder, b.verdict))
	}) {
		t.Errorf("checks are not grouped by verdict in the order %q", verdictOrder)
	}
	for _, c := range checks {
		if !slices.Contains(verdictOrder, c.verdict) || c.description == "" || c.fn == nil ||
			(c.rule != "") != (c.verdict == Insecure) {
			t.Errorf("check %q: needs a verdict, a description, a function, and a rule only if insecure", c.classification())
		}
	}
}

func TestClassify(t *testing.T) {
	target := ethrpc.Address{19: 0x70}
	receiver := ethrpc.Address{19: 0xee}
	newCheck := func(verdict Verdict, description string, fn func(c call) (bool, error)) check {
		c := check{verdict: verdict, description: description}
		if verdict == Insecure {
			c.rule = "R-4.1"
		}
		c.fn = func(_ context.Context, _ *env, _ *safeID, call call) (bool, error) { return fn(call) }
		return c
	}
	to := func(address ethrpc.Address) func(c call) (bool, error) {
		return func(c call) (bool, error) { return c.to == address, nil }
	}
	always := func(call) (bool, error) { return true, nil }
	match := func(verdict Verdict, description string) check { return newCheck(verdict, description, always) }
	abstain := newCheck(Insecure, "abstains", func(call) (bool, error) { return false, nil })
	errCheck := errors.New("check failed")
	fail := newCheck(Insecure, "fails", func(call) (bool, error) { return false, errCheck })
	failSecure := newCheck(Secure, "fails", func(call) (bool, error) { return false, errCheck })

	secure := match(Secure, "signs a message")
	insecure := match(Insecure, "adds an owner")
	// A transaction whose call is to target, and that pays an ether refund to
	// receiver.
	refunded := safenet.SafeTransaction{To: target, GasPrice: big.NewInt(1), RefundReceiver: receiver}
	tests := []struct {
		name   string
		tx     safenet.SafeTransaction
		checks []check
		want   Classification
		err    error
	}{
		{"no checks", safenet.SafeTransaction{}, nil, Classification{}, nil},
		{"all abstain", safenet.SafeTransaction{}, []check{abstain, abstain}, Classification{}, nil},
		{
			"first match wins",
			safenet.SafeTransaction{},
			[]check{abstain, insecure, match(Insecure, "removes an owner")},
			insecure.classification(),
			nil,
		},
		{"insecure before secure", safenet.SafeTransaction{}, []check{abstain, insecure, secure}, insecure.classification(), nil},
		{"error", safenet.SafeTransaction{}, []check{abstain, fail, insecure}, Classification{}, errCheck},
		{"match before error", safenet.SafeTransaction{}, []check{insecure, fail}, insecure.classification(), nil},
		{"error before match", safenet.SafeTransaction{}, []check{fail, secure}, Classification{}, errCheck},
		{"secure error", safenet.SafeTransaction{}, []check{failSecure, secure}, Classification{}, errCheck},
		{"secure match before error", safenet.SafeTransaction{}, []check{secure, failSecure}, secure.classification(), nil},
		{
			"insecure on any call",
			refunded,
			[]check{newCheck(Insecure, "pays a refund", to(receiver)), secure},
			Classification{Verdict: Insecure, Rule: "R-4.1", Description: "pays a refund"},
			nil,
		},
		{
			"secure needs every call",
			refunded,
			[]check{newCheck(Secure, "calls the target", to(target))},
			Classification{},
			nil,
		},
		{
			"secure calls of different classes",
			refunded,
			[]check{
				newCheck(Secure, "calls the target", to(target)),
				newCheck(Secure, "pays a refund", to(receiver)),
				secure,
			},
			Classification{Verdict: Secure, Description: "calls the target; pays a refund"},
			nil,
		},
		{
			"secure calls of one class",
			refunded,
			[]check{secure, newCheck(Secure, "calls the target", to(target))},
			secure.classification(),
			nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := classify(t.Context(), test.checks, nil, request(test.tx))
			if got != test.want || !errors.Is(err, test.err) {
				t.Errorf("classify() = %+v, %v; want %+v, %v", got, err, test.want, test.err)
			}
		})
	}
}

// testSafeBlock is the Safe chain block of the requests that request returns.
const testSafeBlock = 42

// request returns a request for tx, proposed after testSafeBlock on the Safe's
// chain, with a missing chain ID set to that of Ethereum Mainnet, and any other
// missing amounts set to zero.
func request(tx safenet.SafeTransaction) *safenet.Request {
	if tx.ChainID == nil {
		tx.ChainID = big.NewInt(ethrpc.Mainnet)
	}
	for _, amount := range []**big.Int{&tx.Value, &tx.SafeTxGas, &tx.BaseGas, &tx.GasPrice, &tx.Nonce} {
		if *amount == nil {
			*amount = new(big.Int)
		}
	}
	return &safenet.Request{Proposal: safenet.Proposal{SafeBlock: testSafeBlock, Transaction: tx}}
}

func TestClassifyMissingAmount(t *testing.T) {
	r := request(safenet.SafeTransaction{})
	r.Proposal.Transaction.GasPrice = nil
	if _, err := classify(t.Context(), nil, nil, r); err == nil || !strings.Contains(err.Error(), "gasPrice") {
		t.Errorf("classify() = _, %v; want an error for the missing gasPrice", err)
	}
}

func TestClassifyMissingSafeBlock(t *testing.T) {
	r := request(safenet.SafeTransaction{})
	r.Proposal.SafeBlock = 0
	if _, err := classify(t.Context(), nil, nil, r); err == nil || !strings.Contains(err.Error(), "safeBlock") {
		t.Errorf("classify() = _, %v; want an error for the missing safeBlock", err)
	}
}
