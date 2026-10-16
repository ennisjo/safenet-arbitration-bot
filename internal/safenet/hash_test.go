package safenet

import (
	"math/big"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

func mustParseHash(s string) ethrpc.Hash {
	hash, err := ethrpc.ParseHash(s)
	if err != nil {
		panic(err)
	}
	return hash
}

func TestSafeTransactionHash(t *testing.T) {
	// The vector from the Safenet explorer's hashing tests.
	tx := SafeTransaction{
		ChainID:   big.NewInt(100),
		Safe:      ethrpc.MustParseAddress("0x779720809250AF7931935a192FCD007479C41299"),
		To:        ethrpc.MustParseAddress("0x2dC63c83040669F0aDBa5F832F713152bA862c97"),
		Value:     big.NewInt(100_000_000_000_000_000),
		Operation: OperationCall,
		SafeTxGas: new(big.Int),
		BaseGas:   new(big.Int),
		GasPrice:  new(big.Int),
		Nonce:     big.NewInt(1),
	}
	want := mustParseHash("0xd6a2395bd7bd650df56610d38760d1b4b8073d37db35090ce3c855ef659c1b81")
	if got := tx.Hash(); got != want {
		t.Errorf("Hash: got %s, want %s", got, want)
	}
}

func TestRequestID(t *testing.T) {
	// A request on the deployment in testdata/gnosis.json.
	got := requestID(
		ethrpc.Gnosis,
		fixtureConsensus,
		161285,
		fixtureOracle,
		nil,
		mustParseHash("0xea6f04a866f107949f795b60dbcbe9563ab67ee232bbb14c7b85644dc9fce221"),
	)
	want := mustParseHash("0x062be5de4a4ca7123b0894a48807d09ca0e445c282e48dc3601e141bd32b48cb")
	if got != want {
		t.Errorf("requestID: got %s, want %s", got, want)
	}
}
