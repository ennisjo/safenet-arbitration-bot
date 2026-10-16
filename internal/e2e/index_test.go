package e2e_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ipfs"
)

// TestIndex runs `arbot index` against rulings on anvil, indexing a corpus in
// two steps: up to a given block, then up to the latest finalized block.
func TestIndex(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	s1, s2, s3 := n.Artifacts.Sentinels[0], n.Artifacts.Sentinels[1], n.Artifacts.Sentinels[2]
	charter := n.InstallCharter([]byte("# Safenet Arbitration Charter\n"))

	dir := t.TempDir()
	start, err := n.BlockNumber(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "index.json"), fmt.Sprintf("{\n  \"block\": %d\n}\n", start))

	// A ruling whose context is a document on IPFS.
	document := "The transaction adds an owner, which R-4.1 forbids.\n\n```\naddOwnerWithThreshold\n```\n"
	insecure := n.Propose(nil)
	n.Vote(insecure.Approve(s1), insecure.Deny(s2, "R-4.1"))
	insecure.Resolve(false, "ipfs://"+n.ServeIPFS([]byte(document)).String())
	first := uint64(insecure.Record.BlockNumber)

	// An out-of-scope decision, and a ruling whose context is a document with a
	// header of its own.
	outOfScope := n.Propose(nil)
	n.Vote(outOfScope.Approve(s2), outOfScope.Deny(s3, "R-4.2"))
	outOfScope.MarkOutOfScope("Not a Safe transaction.")
	secure := n.Propose(nil)
	n.Vote(secure.Deny(s1, "R-4.4"), secure.Approve(s2))
	secure.Resolve(true, fmt.Sprintf("---\nprecedents: %s\ntags: allowance\nsummary: A reasonable allowance.\n---\n\n"+
		"Unlike %[1]s, the allowance is reasonable under R-4.4 and R-4.1, and R-4.4 again.\n", insecure.ID))

	// A dispute without a ruling, which isn't indexed.
	frozen := n.Propose(nil)
	n.Vote(frozen.Approve(s1), frozen.Deny(s2, "R-4.1"))

	// Anvil's finalized block is 64 blocks before its latest block.
	n.Mine(64)
	arbot := n.Arbot()

	// Without -w, the corpus files and the index are written to stdout.
	stdout := arbot.Run(t, "index", "-corpus", dir, "-block", fmt.Sprint(first))
	insecureFile := filepath.Join(dir, insecure.ID.String()+".md")
	for _, want := range []string{
		"==> " + insecureFile + " <==\n---\nrequest: " + insecure.ID.String() + "\n",
		fmt.Sprintf("==> %s <==\n{\n  \"block\": %d\n}\n", filepath.Join(dir, "index.json"), first),
	} {
		if !bytes.Contains(stdout, []byte(want)) {
			t.Errorf("index: output doesn't contain %q:\n%s", want, stdout)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("index: got %d files in the corpus, want only the index", len(entries))
	}
	checkIndex(t, dir, uint64(start))

	arbot.Run(t, "index", "-w", "-corpus", dir, "-block", fmt.Sprint(first))
	checkIndex(t, dir, first)
	content := checkHeader(t, n, dir, insecure, charter, "insecure", "R-4.1")
	for _, want := range []string{
		"\n## Votes\n\n- " + s1.String() + ": APPROVED\n- " + s2.String() + `: DENIED, reason "R-4.1"` + "\n",
		"\n- Outcome: insecure\n",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("%s doesn't contain %q:\n%s", insecureFile, want, content)
		}
	}
	// The document that the context references comes last, verbatim.
	want := "\n- Context: " + insecure.Context + ", which references the document below\n\n<!-- DOCUMENT START -->\n---\n\n" + document
	if !strings.HasSuffix(content, want) {
		t.Errorf("%s doesn't end with %q:\n%s", insecureFile, want, content)
	}

	// The rulings after the first are indexed up to the latest finalized block.
	arbot.Run(t, "index", "-w", "-corpus", dir)
	finalized, err := n.FinalizedBlock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Number < secure.Record.BlockNumber {
		t.Fatalf("finalized block %d is before the last ruling", finalized.Number)
	}
	checkIndex(t, dir, uint64(finalized.Number))
	content = checkHeader(t, n, dir, outOfScope, charter, "out-of-scope", "-")
	if want := "\n- Context: the document below\n\n<!-- DOCUMENT START -->\n---\n\nNot a Safe transaction."; !strings.HasSuffix(content, want) {
		t.Errorf("out-of-scope ruling doesn't end with %q:\n%s", want, content)
	}
	checkHeader(t, n, dir, secure, charter, "secure", "R-4.4, R-4.1")
	if entries, _ := os.ReadDir(dir); len(entries) != 4 {
		t.Errorf("index: got %d files in the corpus, want the index and 3 rulings", len(entries))
	}

	// Indexing again changes nothing, and a corpus can't be indexed backwards.
	if stdout := string(arbot.Run(t, "index", "-corpus", dir)); strings.Contains(stdout, ".md <==") {
		t.Errorf("index: indexed rulings again:\n%s", stdout)
	}
	stderr := arbot.Fail(t, 1, "index", "-corpus", dir, "-block", fmt.Sprint(first))
	if !bytes.Contains(stderr, []byte("the corpus is indexed up to block")) {
		t.Errorf("index -block %d: got error %q, want one saying that the corpus is indexed past it", first, stderr)
	}
	arbot.Fail(t, 1, "index", "-corpus", t.TempDir())

	// The tags of a ruling are those of its header, then those of its document's.
	tags := string(arbot.Run(t, "tags", "-corpus", dir, secure.ID.String()))
	wantTags := strings.Join(header(n, secure, charter, "secure", "R-4.4, R-4.1")[1:9], "\n") +
		"\n\nprecedents: " + insecure.ID.String() + "\ntags: allowance\nsummary: A reasonable allowance.\n"
	if tags != wantTags {
		t.Errorf("tags: got\n%s\nwant\n%s", tags, wantTags)
	}
	got := decode[map[string]map[string]string](t, arbot.Run(t, "tags", "-json", "-corpus", dir, insecure.ID.String()))
	if got["header"]["outcome"] != "insecure" || len(got["header"]) != 8 || got["document"] == nil || len(got["document"]) != 0 {
		t.Errorf("tags -json: got %v, want the header's 8 tags and no document tags", got)
	}
	arbot.Fail(t, 1, "tags", "-corpus", dir, frozen.ID.String())
}

// checkIndex checks that the index of the corpus in dir is at block.
func checkIndex(t *testing.T, dir string, block uint64) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("{\n  \"block\": %d\n}\n", block); string(data) != want {
		t.Errorf("index.json: got %q, want %q", data, want)
	}
}

// checkHeader checks the header of the corpus file of the ruling on r of n in
// dir, and returns the file's content.
func checkHeader(t *testing.T, n *e2e.Testnet, dir string, r *e2e.Request, charter ipfs.CID, outcome, rules string) string {
	t.Helper()
	path := filepath.Join(dir, r.ID.String()+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := header(n, r, charter, outcome, rules)
	if got := strings.Split(string(data), "\n")[:len(want)]; !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got header\n%s\nwant\n%s", path, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	return string(data)
}

// header returns the lines of the header of the corpus file of the ruling on r
// of n.
func header(n *e2e.Testnet, r *e2e.Request, charter ipfs.CID, outcome, rules string) []string {
	return []string{
		"---",
		"request: " + r.ID.String(),
		"outcome: " + outcome,
		"rules: " + rules,
		"chain: " + r.Transaction.ChainID.String(),
		"safe: " + r.Transaction.Safe.String(),
		"charter: " + charter.String(),
		"proposed: " + n.Header(r.Block).Time().UTC().Format(time.RFC3339),
		"record: " + r.Record.TransactionHash.String(),
		"---",
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
