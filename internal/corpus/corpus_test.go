package corpus

import (
	"bytes"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/ipfs"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// entry returns an entry for a ruling with the context and document.
func entry(context, document string) *Entry {
	zero := new(big.Int)
	return &Entry{
		Request: &safenet.Request{
			ID: ethrpc.Hash{0: 0x1d},
			Proposal: safenet.Proposal{
				Time: time.Date(2026, 10, 14, 1, 2, 3, 0, time.FixedZone("CEST", 2*60*60)),
				Transaction: safenet.SafeTransaction{
					ChainID: big.NewInt(1), Value: zero, SafeTxGas: zero, BaseGas: zero, GasPrice: zero, Nonce: zero,
				},
			},
			Arbitration: &safenet.Arbitration{
				Outcome: safenet.OutcomeInsecure,
				Context: context,
				Record:  &safenet.Record{TxHash: ethrpc.Hash{0: 0x7e}},
			},
		},
		Charter:  ipfs.ComputeCID([]byte("charter")),
		Document: []byte(document),
	}
}

func TestMarkdownHeader(t *testing.T) {
	text := "Under R-4.1, and R-10 and R-4.1. R-4.5 applies too."
	e := entry(text, text)
	lines := strings.Split(string(e.Markdown()), "\n")
	want := []string{
		"---",
		"request: " + e.Request.ID.String(),
		"outcome: insecure",
		"rules: R-4.1, R-10, R-4.5",
		"chain: 1",
		"safe: " + ethrpc.Address{}.String(),
		"charter: " + e.Charter.String(),
		"proposed: 2026-10-13T23:02:03Z",
		"record: " + ethrpc.Hash{0: 0x7e}.String(),
		"---",
	}
	if len(want) != HeaderLines {
		t.Fatalf("want has %d lines, not HeaderLines", len(want))
	}
	if got := lines[:HeaderLines]; !reflect.DeepEqual(got, want) {
		t.Errorf("header: got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := entry("No rule applies.", "No rule applies.").Rules(); got != nil {
		t.Errorf("Rules: got %q, want none", got)
	}
}

func TestMarkdownDocument(t *testing.T) {
	cid := ipfs.ComputeCID([]byte("ruling"))
	document := "---\ntags: allowance\n---\n\n# Ruling\n\n```\ncode\n```\n\xff"
	for _, test := range []struct {
		name, context, document, want string
	}{
		{"text", "Not a Safe transaction.", "Not a Safe transaction.",
			"- Context: the document below\n\n" + DocumentStart + "Not a Safe transaction."},
		{"CID", "ipfs://" + cid.String(), document,
			"- Context: ipfs://" + cid.String() + ", which references the document below\n\n" + DocumentStart + document},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := string(entry(test.context, test.document).Markdown())
			if !strings.HasSuffix(got, "\n"+test.want) {
				t.Errorf("got\n%s\nwant it to end with\n%s", got, test.want)
			}
		})
	}
}

func TestTags(t *testing.T) {
	e := entry("Under R-4.1.", "---\nprecedents: 0x01, 0x02\ntags: owner-change\nsummary: Adds an owner: insecure.\n---\n\nUnder R-4.1.\n")
	header, document, err := Tags(e.Markdown())
	if err != nil {
		t.Fatal(err)
	}
	if len(header) != HeaderLines-2 || header[0] != (Tag{"request", e.Request.ID.String()}) || header[2] != (Tag{"rules", "R-4.1"}) {
		t.Errorf("header: got %v", header)
	}
	want := []Tag{{"precedents", "0x01, 0x02"}, {"tags", "owner-change"}, {"summary", "Adds an owner: insecure."}}
	if !reflect.DeepEqual(document, want) {
		t.Errorf("document: got %v, want %v", document, want)
	}

	// A document without a header, or with a malformed one, has no tags, even if
	// lines of it look like tags.
	for name, doc := range map[string]string{
		"no header":             "Note:\n---\ntags: x\n---\n",
		"bad document line":     "---\ntags: x\n- tag\n---\n",
		"unterminated document": "---\ntags: x\n",
	} {
		if _, document, err := Tags(entry("x", doc).Markdown()); err != nil || document != nil {
			t.Errorf("%s: got %v, %v, want no tags", name, document, err)
		}
	}

	// The file's own header must be well-formed.
	for name, content := range map[string]string{
		"no header":    "# Ruling\n",
		"unterminated": "---\nrequest: 0x01\n",
		"no document":  "---\nrequest: 0x01\n---\n\n# Ruling\n",
		"bad line":     "---\nrequest 0x01\n---\n\n" + DocumentStart,
	} {
		if _, _, err := Tags([]byte(content)); err == nil {
			t.Errorf("%s: got no error", name)
		}
	}
}

func TestContextCID(t *testing.T) {
	cid := ipfs.ComputeCID([]byte("ruling"))
	for context, want := range map[string]bool{
		"ipfs://" + cid.String():  true,
		cid.String():              true,
		"See " + cid.String():     false,
		"Not a Safe transaction.": false,
	} {
		got, ok := ContextCID(context)
		if ok != want || (ok && got != cid) {
			t.Errorf("ContextCID(%q): got %s, %t, want %t", context, got, ok, want)
		}
	}
}

func TestIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, IndexFile), Index{Block: 42}.Marshal(), 0o644); err != nil {
		t.Fatal(err)
	}
	index, err := ReadIndex(dir)
	if err != nil || index.Block != 42 {
		t.Errorf("ReadIndex: got %+v, %v, want block 42", index, err)
	}
	if got := (Index{Block: 42}).Marshal(); !bytes.Equal(got, []byte("{\n  \"block\": 42\n}\n")) {
		t.Errorf("Marshal: got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, IndexFile), []byte(`{"blocks": 42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIndex(dir); err == nil {
		t.Errorf("ReadIndex: got no error for an unknown field")
	}
}
