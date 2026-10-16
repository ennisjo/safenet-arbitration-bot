package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/safe-research/safenet-arbitration-bot/internal/corpus"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/ipfs"
)

// indexCommand adds the rulings recorded onchain since the corpus was last
// indexed to the corpus.
func indexCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("index")
	dir := flags.String("corpus", "corpus", "the corpus `directory`")
	block := flags.Uint64("block", 0, "the last Gnosis Chain block to index (default: the latest finalized block)")
	write := flags.Bool("w", false, "write the new corpus files and the index to the corpus directory, instead of stdout")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s index [flags]\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Indexes the rulings and out-of-scope decisions that the arbitrator recorded after the block in the corpus's %s,\n", corpus.IndexFile)
		fmt.Fprintf(flags.Output(), "up to -block, into one corpus file each, and moves the index to -block.\n")
		fmt.Fprintf(flags.Output(), "It writes the new files and the index to stdout, or with -w, to the corpus directory.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}

	index, err := corpus.ReadIndex(*dir)
	if err != nil {
		return err
	}
	sn := openSafenet(e.cfg, ethrpc.NewDialer(e.cfg.RPCs))
	if *block == 0 {
		if *block, err = sn.FinalizedBlock(ctx); err != nil {
			return err
		}
	}
	if *block < index.Block {
		return fmt.Errorf("the corpus is indexed up to block %d, after block %d", index.Block, *block)
	}

	var entries []*corpus.Entry
	if *block > index.Block {
		requests, err := sn.Rulings(ctx, index.Block+1, *block)
		if err != nil {
			return err
		}
		gateway := ipfs.NewClient(e.cfg.IPFS)
		for _, request := range requests {
			entry := &corpus.Entry{Request: request}
			if request.Proposal.EthereumBlock == 0 || request.Charter == "" {
				return fmt.Errorf("request %s has no Charter name or Ethereum Mainnet block", request.ID)
			}
			if entry.Charter, _, err = charterCID(ctx, e, request.Charter, ethrpc.BlockNumber(request.Proposal.EthereumBlock)); err != nil {
				return fmt.Errorf("request %s: %w", request.ID, err)
			}
			entry.Document = []byte(request.Arbitration.Context)
			if cid, ok := corpus.ContextCID(request.Arbitration.Context); ok {
				if entry.Document, err = gateway.Fetch(ctx, cid); err != nil {
					return fmt.Errorf("request %s: %w", request.ID, err)
				}
			}
			entries = append(entries, entry)
		}
	}

	files := make(map[string][]byte)
	var paths []string
	for _, entry := range entries {
		path := filepath.Join(*dir, corpus.Filename(entry.Request.ID))
		files[path] = entry.Markdown()
		paths = append(paths, path)
	}
	indexPath := filepath.Join(*dir, corpus.IndexFile)
	files[indexPath] = corpus.Index{Block: *block}.Marshal()
	paths = append(paths, indexPath)

	if !*write {
		for i, path := range paths {
			if i > 0 {
				fmt.Fprintln(e.stdout)
			}
			fmt.Fprintf(e.stdout, "==> %s <==\n", path)
			if _, err := e.stdout.Write(files[path]); err != nil {
				return err
			}
		}
		return nil
	}

	// A corpus file that is already there was indexed before, so it is kept if it
	// is the same, and is an error if it isn't, as it was changed since. Every file
	// is checked before any is written.
	var writes []string
	for _, path := range paths[:len(paths)-1] {
		existing, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			writes = append(writes, path)
			continue
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, files[path]) {
			return fmt.Errorf("%s already exists, with other content", path)
		}
	}
	// The index is written last, so that it never covers a ruling whose file wasn't
	// written.
	for _, path := range append(writes, indexPath) {
		if err := os.WriteFile(path, files[path], 0o644); err != nil {
			return err
		}
	}
	return nil
}
