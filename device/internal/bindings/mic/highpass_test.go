package mic

import (
	"math"
	"testing"
)

// gainDB runs a sine of freq Hz through channel 0 of a fresh filter for 1s
// and returns the output/input RMS ratio over the last half, in dB, so the
// start-up transient is excluded.
func gainDB(t *testing.T, freq float64) float64 {
	t.Helper()
	h := newDonutHighpass()
	const n = hpSampleRate
	var in, out float64
	for i := 0; i < n; i++ {
		x := int32(math.Round(1e6 * math.Sin(2*math.Pi*freq*float64(i)/hpSampleRate)))
		y := h.sample(0, x)
		if i >= n/2 {
			in += float64(x) * float64(x)
			out += float64(y) * float64(y)
		}
	}
	return 10 * math.Log10(out/in)
}

func TestHighpassCutsMainsHum(t *testing.T) {
	// 100 Hz is the Dot 3's loudest noise component. A 4th-order Butterworth
	// at 150 Hz gives -14.3 dB there; a 2nd-order would give -8.
	for _, f := range []float64{50, 100} {
		if g := gainDB(t, f); g > -12 {
			t.Errorf("%v Hz: %.1f dB, want below -12", f, g)
		}
	}
}

func TestHighpassLeavesSpeechAlone(t *testing.T) {
	// 200 Hz is the low end of a voiced fundamental; above 300 Hz the filter
	// must be inaudible, since that is where the wake word listens.
	for f, floor := range map[float64]float64{200: -1, 300: -0.1, 1000: -0.05, 3500: -0.05, 7000: -0.05} {
		g := gainDB(t, f)
		if g < floor || g > 0.05 {
			t.Errorf("%v Hz: %.3f dB, want between %v and +0.05", f, g, floor)
		}
	}
}

func TestHighpassRemovesDC(t *testing.T) {
	h := newDonutHighpass()
	var y int32
	for i := 0; i < hpSampleRate/2; i++ {
		y = h.sample(0, 1_000_000)
	}
	if y != 0 {
		t.Fatalf("constant input still reads %d after 0.5s, want 0", y)
	}
}

func TestHighpassSilenceStaysSilent(t *testing.T) {
	// A muted ADC delivers digital zero. The filter must add nothing to it,
	// and its state must reach exactly zero rather than decaying into
	// subnormals forever.
	h := newDonutHighpass()
	for i := 0; i < 100; i++ {
		h.sample(0, 5_000_000)
	}
	for i := 0; i < 2*hpSampleRate; i++ {
		h.sample(0, 0)
	}
	for s := range h.z[0] {
		if h.z[0][s] != [2]float64{} {
			t.Fatalf("stage %d state %v after 2s of silence, want exact zero", s, h.z[0][s])
		}
	}
	if y := h.sample(0, 0); y != 0 {
		t.Fatalf("silence reads %d, want 0", y)
	}
}

func TestHighpassClampsInsteadOfWrapping(t *testing.T) {
	// A full-scale step overshoots the 24-bit range. The output must pin at
	// the rail with the right sign; a wrapped sample is a full-scale click.
	h := newDonutHighpass()
	for i := 0; i < hpSampleRate/10; i++ {
		h.sample(0, s24Min)
	}
	if y := h.sample(0, s24Max); y != s24Max {
		t.Fatalf("full-scale step gave %d, want the positive rail %d", y, s24Max)
	}
	for i := 0; i < hpSampleRate/10; i++ {
		h.sample(1, s24Max)
	}
	if y := h.sample(1, s24Min); y != s24Min {
		t.Fatalf("full-scale step down gave %d, want the negative rail %d", y, s24Min)
	}
}

func TestHighpassChannelsAreIndependent(t *testing.T) {
	h := newDonutHighpass()
	for i := 0; i < 1000; i++ {
		h.sample(0, int32(1e6*math.Sin(float64(i))))
		if y := h.sample(1, 0); y != 0 {
			t.Fatalf("sample %d: channel 1 reads %d from channel 0's signal", i, y)
		}
	}
}

func TestRepackFiltersEachChannelOnce(t *testing.T) {
	// Slots sharing a Dot 3 channel must carry the same filtered sample, and
	// that sample must equal the channel filtered on its own, which it would
	// not if the filter ran once per slot.
	const frames = 2560
	in := donutCapture(frames, func(f, c int) int32 {
		return int32(1e5 * math.Sin(2*math.Pi*float64((c+1)*400*f)/hpSampleRate))
	})
	r := repacker{hp: newDonutHighpass()}
	var b []byte
	r.push(in, func(x []byte) { b = x })

	ref := newDonutHighpass()
	for f := 0; f < frames; f++ {
		var want [donutChannels]int32
		for c := range want {
			want[c] = ref.sample(c, int32(1e5*math.Sin(2*math.Pi*float64((c+1)*400*f)/hpSampleRate)))
		}
		for slot, ch := range donutSlot {
			o := f*outFrameBytes + slot*outSampleSize
			w := int32(0)
			if ch >= 0 {
				w = want[ch]
			}
			if v := s24(b[o : o+3]); v != w {
				t.Fatalf("frame %d slot %d = %d, want %d", f, slot, v, w)
			}
		}
	}
}

func TestRepackFilterStateCarriesAcrossReads(t *testing.T) {
	// However a capture is split into reads, the filtered output must equal
	// filtering it whole: state that reset per read would click every 160ms.
	in := donutCapture(3000, func(f, c int) int32 {
		return int32(2e6*math.Sin(float64(f)*0.04)) + int32(c)*1000
	})
	var whole []byte
	a := repacker{hp: newDonutHighpass()}
	a.push(in, func(x []byte) { whole = append(whole, x...) })
	for _, step := range []int{1, 7, 16, 2560 * donutFrameBytes / 5} {
		var pieces []byte
		b := repacker{hp: newDonutHighpass()}
		for i := 0; i < len(in); i += step {
			b.push(in[i:min(i+step, len(in))], func(x []byte) { pieces = append(pieces, x...) })
		}
		if string(whole) != string(pieces) {
			t.Fatalf("split into %d-byte reads: filtered output differs from one read", step)
		}
	}
}
