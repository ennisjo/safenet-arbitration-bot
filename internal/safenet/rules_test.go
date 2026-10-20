package safenet

import (
	"reflect"
	"testing"
)

func TestCitedRules(t *testing.T) {
	tests := []struct {
		reason string
		want   []string
	}{
		{"", []string{}},
		{"R-4.1", []string{"R-4.1"}},
		{"R-4.1, R-4.2", []string{"R-4.1", "R-4.2"}},
		{"R-4.2,R-4.1", []string{"R-4.2", "R-4.1"}},
		{" R-4.1 ,\tR-4.2 ", []string{"R-4.1", "R-4.2"}},
		{"R-4.1, R-4.1", []string{"R-4.1"}},
		{"R-4, R-4.6.1", []string{"R-4", "R-4.6.1"}},
		{"R-4.1,, R-4.2,", []string{"R-4.1", "R-4.2"}},
		{"R-4.1 R-4.2", []string{}},
		{"R-4.1; R-4.2", []string{}},
		{"violates R-4.1", []string{}},
		{"R-4.1, drains the Safe", []string{"R-4.1"}},
		{"r-4.1, R-4., R-.1, R-4.1a", []string{}},
		{"insecure", []string{}},
	}
	for _, test := range tests {
		if got := CitedRules(test.reason); !reflect.DeepEqual(got, test.want) {
			t.Errorf("CitedRules(%q) = %q, want %q", test.reason, got, test.want)
		}
	}
}

func TestDisputedRules(t *testing.T) {
	votes := []Commitment{
		{Vote: VoteApproved, Reason: "R-4.0"},
		{Vote: VoteDenied, Reason: "R-4.2, R-4.1"},
		{Vote: VotePending},
		{Vote: VoteDenied, Reason: "R-4.1, R-4.3"},
		{Vote: VoteDenied, Reason: "no rule"},
	}
	want := []string{"R-4.2", "R-4.1", "R-4.3"}
	if got := DisputedRules(votes); !reflect.DeepEqual(got, want) {
		t.Errorf("DisputedRules = %q, want %q", got, want)
	}
	if got := DisputedRules(votes[:1]); !reflect.DeepEqual(got, []string{}) {
		t.Errorf("DisputedRules with no DENIED vote = %q, want none", got)
	}
}
