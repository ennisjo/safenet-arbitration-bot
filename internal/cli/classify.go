package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// classify classifies a Safenet request with the deterministic checks of the
// Charter's rules.
func classify(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("classify")
	asJSON := flags.Bool("json", false, "write the classification as JSON")
	list := flags.Bool("list", false, "list the checks, in the order that they run, instead of classifying a request")
	requestFile := flags.String("request-file", "", "read the request from the JSON file at `path`, as written by arbot info -json, instead of fetching it")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s classify [flags] <request-id>\n", e.progname)
		fmt.Fprintf(flags.Output(), "       %s classify [flags] -request-file <path>\n", e.progname)
		fmt.Fprintf(flags.Output(), "       %s classify [flags] -list\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Classifies a Safenet request with the deterministic checks of the Charter's rules, as one of:\n\n")
		fmt.Fprintf(flags.Output(), "  unclassified\n  secure\n  insecure  <rule>  <description>\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, -1); err != nil {
		return err
	}
	// A request file or -list replaces the request ID.
	if *list && *requestFile != "" {
		return usageError("-list and -request-file are mutually exclusive")
	}
	if n := flags.NArg(); n != 1 && !*list && *requestFile == "" || n != 0 && (*list || *requestFile != "") {
		flags.Usage()
		return usageError("")
	}
	if *list {
		return listChecks(e, *asJSON)
	}

	// The request and the checks share connections to chains, such as to Gnosis
	// Chain for a Safe on it.
	dial := ethrpc.NewDialer(e.cfg.RPCs)
	var request *safenet.Request
	var err error
	if *requestFile != "" {
		request, err = readRequest(*requestFile)
	} else {
		var id ethrpc.Hash
		if id, err = ethrpc.ParseHash(flags.Arg(0)); err != nil {
			return usageError(fmt.Sprintf("request ID %q: %v", flags.Arg(0), err))
		}
		request, err = openSafenet(e.cfg, dial).Request(ctx, id)
	}
	if err != nil {
		return err
	}
	c, err := checks.Classify(ctx, dial, request)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(e.stdout, c)
	}
	_, err = fmt.Fprintln(e.stdout, c)
	return err
}

// listChecks writes the classification of each check, as a table or as a JSON
// array, in the order that the checks run. The table has "-" for an empty rule.
func listChecks(e *env, asJSON bool) error {
	list := checks.List()
	if asJSON {
		return writeJSON(e.stdout, list)
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VERDICT\tRULE\tDESCRIPTION")
	for _, c := range list {
		rule := c.Rule
		if rule == "" {
			rule = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", c.Verdict, rule, c.Description)
	}
	return w.Flush()
}
