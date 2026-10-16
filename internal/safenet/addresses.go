package safenet

import "github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"

// The addresses of the SentinelOracle and Consensus deployments on Gnosis Chain
// that commands use by default.
var (
	DefaultOracle    = ethrpc.MustParseAddress("0x4F61B8832978e83b80D69551AEf07557DBE41d03")
	DefaultConsensus = ethrpc.MustParseAddress("0xc855761D619f6002923507cE68B84d7689C2aa96")
)
