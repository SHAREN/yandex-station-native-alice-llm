package bridge

import "testing"

func TestExternalStreamIDsFollowStockServerConvention(t *testing.T) {
	externalStreamSequence.Store(0)
	defer externalStreamSequence.Store(0)

	for i, want := range []uint32{2, 4, 6, 8} {
		if got := nextExternalStreamID(); got != want {
			t.Fatalf("stream %d = %d, want %d", i, got, want)
		}
	}
}
