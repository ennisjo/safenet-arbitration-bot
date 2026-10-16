package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/safe-research/safenet-arbitration-bot/internal/corpus"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// tags lists the tags of the corpus file of a ruling.
func tags(_ context.Context, e *env, args []string) error {
	flags := e.flagSet("tags")
	asJSON := flags.Bool("json", false, "write the tags as JSON")
	dir := flags.String("corpus", "corpus", "the corpus `directory`")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s tags [flags] <request-id>\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Lists the tags of the ruling on a request in the corpus: those of its header, which arbot index wrote,\n")
		fmt.Fprintf(flags.Output(), "then those of the header that its ruling document starts with, if it has one.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 1); err != nil {
		return err
	}
	id, err := ethrpc.ParseHash(flags.Arg(0))
	if err != nil {
		return usageError(fmt.Sprintf("request ID %q: %v", flags.Arg(0), err))
	}

	path := filepath.Join(*dir, corpus.Filename(id))
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	header, document, err := corpus.Tags(content)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if *asJSON {
		object := func(tags []corpus.Tag) map[string]string {
			m := make(map[string]string)
			for _, tag := range tags {
				m[tag.Key] = tag.Value
			}
			return m
		}
		return writeJSON(e.stdout, map[string]any{"header": object(header), "document": object(document)})
	}
	for _, tag := range header {
		fmt.Fprintf(e.stdout, "%s: %s\n", tag.Key, tag.Value)
	}
	if len(document) > 0 {
		fmt.Fprintln(e.stdout)
	}
	for _, tag := range document {
		fmt.Fprintf(e.stdout, "%s: %s\n", tag.Key, tag.Value)
	}
	return nil
}
