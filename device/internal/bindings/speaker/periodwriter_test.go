package speaker

import (
	"bytes"
	"testing"
)

func TestPeriodWriterWritesOnlyWholePeriods(t *testing.T) {
	const period = 768 * 4 // Dot 3: 768 frames, stereo S16
	pw := newPeriodWriter(period)
	var got []byte
	calls := 0
	w := func(b []byte) error {
		if len(b) != period {
			t.Fatalf("write of %d bytes, want exactly %d", len(b), period)
		}
		calls++
		got = append(got, b...)
		return nil
	}
	var sent []byte
	for i := 0; i < 9; i++ { // nine 2048-frame mixed periods
		mixed := make([]byte, 2048*4)
		for j := range mixed {
			mixed[j] = byte(i*31 + j)
		}
		sent = append(sent, mixed...)
		if err := pw.write(mixed, w); err != nil {
			t.Fatal(err)
		}
	}
	// 9 x 2048 = 18432 frames = exactly 24 periods of 768: nothing left over,
	// nothing lost, nothing repeated, in order.
	if calls != 24 || len(pw.pending) != 0 {
		t.Fatalf("%d writes, %d bytes pending; want 24 and 0", calls, len(pw.pending))
	}
	if !bytes.Equal(got, sent) {
		t.Fatal("written audio differs from the audio sent")
	}
}

func TestPeriodWriterLeavesBiscuitsWritesAlone(t *testing.T) {
	// biscuit: 1024-frame period, 2048-frame mixed period — two writes, no
	// carry, and the slices written are the caller's own (no copy).
	const period = 1024 * 4
	pw := newPeriodWriter(period)
	mixed := make([]byte, 2048*4)
	var ptrs []*byte
	if err := pw.write(mixed, func(b []byte) error { ptrs = append(ptrs, &b[0]); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(ptrs) != 2 || ptrs[0] != &mixed[0] || ptrs[1] != &mixed[period] {
		t.Fatal("biscuit's writes changed: want two in-place period writes")
	}
}
