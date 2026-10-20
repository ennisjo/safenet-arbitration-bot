package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
)

func TestClassify(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("config.json", "{}")
	// The request is on a network that the Charter doesn't cover, which the checks
	// decide without reading the chain.
	request := write("request.json", `{
		"id": "0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb",
		"state": "FROZEN",
		"proposal": {
			"safeBlock": 1,
			"transaction": {"chainId": 10, "value": 0, "safeTxGas": 0, "baseGas": 0, "gasPrice": 0, "nonce": 0}
		}
	}`)

	// The list is that of the checks package, as a table or as JSON.
	listJSON, err := json.MarshalIndent(checks.List(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"-request-file", request}, 0, "insecure  R-4.0  Safe on a network that the Charter doesn't cover\n", ""},
		{
			[]string{"-json", "-request-file", request},
			0,
			"{\n  \"verdict\": \"insecure\",\n  \"rule\": \"R-4.0\",\n  \"description\": \"Safe on a network that the Charter doesn't cover\"\n}\n",
			"",
		},
		{[]string{"-request-file", write("unknown.json", `{"stat": "FROZEN"}`)}, 1, "", `unknown field "stat"`},
		{[]string{"-request-file", write("state.json", `{"state": "frozen"}`)}, 1, "", `unknown value "frozen"`},
		{[]string{"-request-file", write("null.json", `null`)}, 1, "", "no request"},
		{[]string{"-request-file", write("incomplete.json", `{"state": "FROZEN"}`)}, 1, "", "safe transaction has no chainId"},
		{
			[]string{"-request-file", write("no-block.json", `{"proposal": {"transaction": {"chainId": 10, "value": 0, "safeTxGas": 0, "baseGas": 0, "gasPrice": 0}}}`)},
			1,
			"",
			"proposal has no safeBlock",
		},
		{[]string{"-request-file", filepath.Join(dir, "missing.json")}, 1, "", "no such file"},
		{[]string{"-request-file", request, "0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb"}, 2, "", "Usage:"},
		{[]string{}, 2, "", "Usage:"},
		{[]string{"-json", "-list"}, 0, string(listJSON) + "\n", ""},
		{[]string{"-list", "-request-file", request}, 2, "", "mutually exclusive"},
		{[]string{"-list", "0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb"}, 2, "", "Usage:"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "classify"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.String() != test.stdout || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("classify %v: got status %d, stdout %q, stderr %q; want %d, %q, and stderr containing %q",
				test.args, code, stdout.String(), stderr.String(), test.code, test.stdout, test.stderr)
		}
	}
}

func TestClassifyList(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"arbot", "-config", config, "classify", "-list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("classify -list: got status %d, stderr %q", code, stderr.String())
	}

	// The table has a row for each check, in the order that they run, with its
	// columns separated by at least two spaces.
	want := [][]string{{"VERDICT", "RULE", "DESCRIPTION"}}
	for _, c := range checks.List() {
		rule := c.Rule
		if rule == "" {
			rule = "-"
		}
		want = append(want, []string{c.Verdict.String(), rule, c.Description})
	}
	var got [][]string
	for line := range strings.Lines(stdout.String()) {
		got = append(got, regexp.MustCompile(`  +`).Split(strings.TrimSuffix(line, "\n"), -1))
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("classify -list: got rows %q, want %q", got, want)
	}
}
