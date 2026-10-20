// Package checks classifies Safenet requests with deterministic checks of the
// Safenet Arbitration Charter's rules.
package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// Verdict is how a request is classified.
type Verdict string

const (
	// Unclassified means that no check decides the request.
	Unclassified Verdict = ""
	Secure       Verdict = "secure"
	Insecure     Verdict = "insecure"
)

// String returns the verdict's name, which is "unclassified" for Unclassified.
func (v Verdict) String() string {
	if v == Unclassified {
		return "unclassified"
	}
	return string(v)
}

// MarshalJSON encodes Unclassified as null, and other verdicts as their names.
func (v Verdict) MarshalJSON() ([]byte, error) {
	if v == Unclassified {
		return []byte("null"), nil
	}
	return json.Marshal(string(v))
}

// Classification is a request's verdict, with the ID of the Charter rule that
// an insecure request breaks, and a short description of the class of requests
// that gets the verdict.
type Classification struct {
	Verdict     Verdict `json:"verdict"`
	Rule        string  `json:"rule,omitempty"`
	Description string  `json:"description,omitempty"`
}

// String returns the classification as a line of text: the verdict, followed by
// the rule and the description if present, separated by two spaces.
func (c Classification) String() string {
	fields := []string{c.Verdict.String()}
	for _, field := range []string{c.Rule, c.Description} {
		if field != "" {
			fields = append(fields, field)
		}
	}
	return strings.Join(fields, "  ")
}

// check is a deterministic check of a class of calls that a Safe makes, which
// gets its classification: a verdict, the ID of the Charter rule that an
// insecure call breaks, and a short description of the class.
type check struct {
	verdict     Verdict
	rule        string
	description string
	// fn reports whether a call that the Safe makes is in the check's class. The
	// check abstains on the call if it isn't.
	fn func(ctx context.Context, env *env, safe *safeID, c call) (bool, error)
}

// env is the environment that checks run in, from which they read the state of
// the Safe's chain.
type env struct {
	// dial connects to a chain.
	dial ethrpc.Dialer
	// block is the last block on the Safe's chain before the proposal. Checks read
	// the chain's state at this block, without the effects of the transaction or of
	// anything later (Charter § 2.8 and § 3.6).
	block uint64
}

func (c check) classification() Classification {
	return Classification{Verdict: c.verdict, Rule: c.rule, Description: c.description}
}

// checks are the checks that Classify runs, in the order that it runs them.
// They are grouped by verdict, in verdictOrder.
var checks = []check{
	offNetwork,
	unsupportedSafe,
	addOwnerCheck,
	removeOwnerCheck,
	swapOwnerCheck,
	changeThresholdCheck,
	enableModuleCheck,
	disableModuleCheck,
	setGuardCheck,
	setFallbackHandlerCheck,
	setModuleGuardCheck,
	emptyMultiSendCheck,
	invalidMultiSendCheck,
	allowedDisableModuleCheck,
	allowedSetFallbackHandlerCheck,
	emptySelfCallCheck,
	emptyZeroAddressCallCheck,
}

// verdictOrder is the order of the verdicts of checks: a request that fails a
// rule is insecure, whatever else holds for it (Charter § 3.7). There are no
// out-of-scope checks, since R-4.0 applies to every transaction, on any
// network, so every transaction is in the scope of Article I (§ 3.9).
var verdictOrder = []Verdict{Insecure, Secure}

// List returns the classifications of the checks, in the order that Classify
// runs them.
func List() []Classification {
	list := []Classification{}
	for _, c := range checks {
		list = append(list, c.classification())
	}
	return list
}

// Classify classifies request. It runs the insecure checks, in order, on each
// call that the Safe makes, and returns the classification of the first one
// that matches a call. If none do, and a secure check matches every call, the
// request is secure, with the descriptions of the first secure check that
// matches each call. Otherwise, it is Unclassified. Checks read the state of
// the Safe's chain at the proposal's SafeBlock, connecting with dial.
func Classify(ctx context.Context, dial ethrpc.Dialer, request *safenet.Request) (Classification, error) {
	return classify(ctx, checks, dial, request)
}

// classify is Classify with checks, which must be grouped by verdict in
// verdictOrder. It returns an error if the proposal has no SafeBlock, as a
// request read from a file can lack, rather than read the genesis state.
func classify(ctx context.Context, checks []check, dial ethrpc.Dialer, request *safenet.Request) (Classification, error) {
	tx, err := components(&request.Proposal.Transaction)
	if err != nil {
		return Classification{}, err
	}
	if request.Proposal.SafeBlock == 0 {
		return Classification{}, errors.New("proposal has no safeBlock")
	}
	env := &env{dial: dial, block: request.Proposal.SafeBlock}
	secure := slices.IndexFunc(checks, func(c check) bool { return c.verdict == Secure })
	if secure < 0 {
		secure = len(checks)
	}
	for _, c := range checks[:secure] {
		for _, call := range tx.calls {
			match, err := c.match(ctx, env, &tx.safe, call)
			if err != nil {
				return Classification{}, err
			}
			if match {
				return c.classification(), nil
			}
		}
	}

	var descriptions []string
	for _, call := range tx.calls {
		i, err := firstMatch(ctx, checks[secure:], env, &tx.safe, call)
		if err != nil || i < 0 {
			return Classification{}, err
		}
		if d := checks[secure+i].description; !slices.Contains(descriptions, d) {
			descriptions = append(descriptions, d)
		}
	}
	return Classification{Verdict: Secure, Description: strings.Join(descriptions, "; ")}, nil
}

// firstMatch returns the index of the first of checks that matches c, or -1 if
// none do.
func firstMatch(ctx context.Context, checks []check, env *env, safe *safeID, c call) (int, error) {
	for i, check := range checks {
		if match, err := check.match(ctx, env, safe, c); err != nil || match {
			return i, err
		}
	}
	return -1, nil
}

// match reports whether c is in the check's class.
func (c check) match(ctx context.Context, env *env, safe *safeID, call call) (bool, error) {
	match, err := c.fn(ctx, env, safe, call)
	if err != nil {
		return false, fmt.Errorf("check %q: %w", c.classification(), err)
	}
	return match, nil
}
