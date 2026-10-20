package safenet

import (
	"regexp"
	"slices"
	"strings"
)

// rulePattern matches a Charter rule identifier, such as R-4.1.
var rulePattern = regexp.MustCompile(`^R-[0-9]+(?:\.[0-9]+)*$`)

// CitedRules returns the rule identifiers that a vote's reason cites. A reason
// is a comma-separated list of rule identifiers, such as "R-4.1, R-4.2": each
// part of it, with the spaces around it trimmed, that is a rule identifier is
// cited, in the order of its first appearance, and any other part cites
// nothing. The oracle doesn't validate reasons (Charter § 2.14), so a cited
// rule may be one that the Charter doesn't define.
func CitedRules(reason string) []string {
	rules := []string{}
	for part := range strings.SplitSeq(reason, ",") {
		rule := strings.TrimSpace(part)
		if rulePattern.MatchString(rule) && !slices.Contains(rules, rule) {
			rules = append(rules, rule)
		}
	}
	return rules
}

// DisputedRules returns the rules that the DENIED votes cite, all of them
// together, in the order of their first citation.
func DisputedRules(votes []Commitment) []string {
	rules := []string{}
	for _, v := range votes {
		if v.Vote != VoteDenied {
			continue
		}
		for _, rule := range CitedRules(v.Reason) {
			if !slices.Contains(rules, rule) {
				rules = append(rules, rule)
			}
		}
	}
	return rules
}
