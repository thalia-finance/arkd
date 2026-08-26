package wallet

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSweepOutputValue pins the fee check the redemption and boarding
// sweeps share. The amounts are unsigned, so a fee above the amount used to
// wrap the remainder past the dust check and drive the output negative;
// that case must be the same clean refusal as a remainder at dust.
func TestSweepOutputValue(t *testing.T) {
	const dust = 546

	tests := []struct {
		name   string
		amount uint64
		fee    uint64
		want   uint64
		refuse bool
	}{{
		name:   "comfortably above dust",
		amount: 10_000,
		fee:    1_000,
		want:   9_000,
	}, {
		name:   "one above dust",
		amount: dust + 1_001,
		fee:    1_000,
		want:   dust + 1,
	}, {
		name:   "exactly dust",
		amount: dust + 1_000,
		fee:    1_000,
		refuse: true,
	}, {
		name:   "fee equals amount",
		amount: 1_000,
		fee:    1_000,
		refuse: true,
	}, {
		// The underflow case: fee above amount wrapped the remainder to
		// ~2^64 and passed the dust check.
		name:   "fee exceeds amount",
		amount: 300,
		fee:    1_000,
		refuse: true,
	}, {
		name:   "nothing to sweep",
		amount: 0,
		fee:    1_000,
		refuse: true,
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sweepOutputValue(tc.amount, tc.fee, dust)
			if tc.refuse {
				require.ErrorIs(t, err, errSweepUnaffordable)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
