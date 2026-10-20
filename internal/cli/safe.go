package cli

import (
	"context"
	"fmt"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safesettings"
)

// safeReport is the JSON output of `arbot safe`.
type safeReport struct {
	Safe      ethrpc.Address `json:"safe"`
	ChainID   uint64         `json:"chainId"`
	SafeBlock uint64         `json:"safeBlock"`
	*safesettings.Settings
}

// safeCommand shows the settings of the Safe of a request before its proposal.
func safeCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("safe")
	asJSON := flags.Bool("json", false, "write the settings as JSON")
	requestFile := flags.String("request-file", "", "read the Safe, its chain, and the block from the request in the JSON file at `path`, as written by arbot info -json")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s safe [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Shows the settings of the Safe of a request, as Charter § 2.10 lists them, read from the storage of\n")
		fmt.Fprintf(out, "the Safe on its chain as of the request's Safe block, without the transaction's own effects or\n")
		fmt.Fprintf(out, "anything later: its singleton, owners, threshold, nonce, enabled modules, fallback handler, guard,\n")
		fmt.Fprintf(out, "and module guard, which only Safe 1.5.0 has. The storage is read with the layout of the Safe versions\n")
		fmt.Fprintf(out, "of § 2.1, so the settings are meaningless for an account that isn't a Safe of one of them, which\n")
		fmt.Fprintf(out, "arbot calls shows. The zero address means that a setting isn't set.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	safe, err := openSafe(ctx, e, *requestFile)
	if err != nil {
		return err
	}
	settings, err := safesettings.Read(ctx, safe.eth, safe.Safe, ethrpc.BlockNumber(safe.SafeBlock))
	if err != nil {
		return err
	}

	report := safeReport{Safe: safe.Safe, ChainID: safe.ChainID, SafeBlock: safe.SafeBlock, Settings: settings}
	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	fmt.Fprintf(e.stdout, "Safe %s on chain %d, as of block %d\n", report.Safe, report.ChainID, report.SafeBlock)
	fmt.Fprintf(e.stdout, "  Singleton         %s\n", settings.Singleton)
	fmt.Fprintf(e.stdout, "  Threshold         %s of %s owners\n", settings.Threshold, settings.OwnerCount)
	fmt.Fprintf(e.stdout, "  Nonce             %s\n", settings.Nonce)
	fmt.Fprintf(e.stdout, "  Fallback handler  %s\n", settings.FallbackHandler)
	fmt.Fprintf(e.stdout, "  Guard             %s\n", settings.Guard)
	fmt.Fprintf(e.stdout, "  Module guard      %s\n", settings.ModuleGuard)
	for _, list := range []struct {
		name    string
		entries []ethrpc.Address
	}{{"Owners", settings.Owners}, {"Modules", settings.Modules}} {
		fmt.Fprintf(e.stdout, "\n%s\n", list.name)
		if len(list.entries) == 0 {
			fmt.Fprintf(e.stdout, "  none\n")
		}
		for _, address := range list.entries {
			fmt.Fprintf(e.stdout, "  %s\n", address)
		}
	}
	return nil
}
