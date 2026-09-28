package mic

import (
	"encoding/binary"
	"testing"
)

// s32 is one Dot 3 sample: 24-bit data left-justified in 32 bits, with junk in
// the low byte to prove it is dropped.
func s32(v int32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(v<<8|0x5a))
	return b
}

// s24 decodes one biscuit sample.
func s24(b []byte) int32 {
	v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
	if v&0x800000 != 0 {
		v |= ^int32(0xFFFFFF)
	}
	return v
}

// donutCapture builds n frames where channel c of frame f carries value(f, c).
func donutCapture(n int, value func(f, c int) int32) []byte {
	out := make([]byte, 0, n*donutFrameBytes)
	for f := 0; f < n; f++ {
		for c := 0; c < donutChannels; c++ {
			out = append(out, s32(value(f, c))...)
		}
	}
	return out
}

func value(f, c int) int32 {
	v := int32(f*10 + c + 1)
	if c == 2 {
		v = -v // negative samples must keep their sign
	}
	return v
}

func TestRepackMakesBiscuitShapedBatches(t *testing.T) {
	const frames = 3 * outFrames
	in := donutCapture(frames, value)

	var r repacker
	var got [][]byte
	// The driver's 256-frame periods, as on hardware.
	for off := 0; off < len(in); off += 256 * donutFrameBytes {
		r.push(in[off:off+256*donutFrameBytes], func(b []byte) { got = append(got, b) })
	}
	if len(got) != 3 {
		t.Fatalf("%d batches, want 3", len(got))
	}
	for bi, b := range got {
		if len(b) != outBatchBytes {
			t.Fatalf("batch %d is %d bytes, want %d", bi, len(b), outBatchBytes)
		}
		for i := 0; i < outFrames; i++ {
			f := bi*outFrames + i
			for slot, ch := range donutSlot {
				o := i*outFrameBytes + slot*outSampleSize
				v := s24(b[o : o+3])
				want := int32(0)
				if ch >= 0 {
					want = value(f, ch)
				}
				if v != want {
					t.Fatalf("batch %d frame %d slot %d = %d, want %d", bi, i, slot, v, want)
				}
			}
		}
	}
	if &got[0][0] == &got[1][0] {
		t.Fatal("batches share a buffer; subscribers would see it overwritten")
	}
}

func TestRepackCarriesPartialFrames(t *testing.T) {
	// Any split, down to single bytes, must give the same output as whole
	// periods.
	in := donutCapture(outFrames, value)
	var whole, bytewise []byte
	var a, b repacker
	a.push(in, func(x []byte) { whole = x })
	for i := range in {
		b.push(in[i:i+1], func(x []byte) { bytewise = x })
	}
	if whole == nil || bytewise == nil {
		t.Fatal("no batch emitted")
	}
	if string(whole) != string(bytewise) {
		t.Fatal("byte-at-a-time input produced a different batch")
	}
}

func TestEveryPerimeterDirectionHasAMic(t *testing.T) {
	// A fixed beam angle picks one of the six perimeter slots; none may be
	// silent, or steering there would mute the Echo.
	for slot := 0; slot < 6; slot++ {
		if donutSlot[slot] < 0 {
			t.Errorf("perimeter slot %d is silent", slot)
		}
	}
	if donutSlot[6] < 0 {
		t.Error("the centre slot (the wake word's input) is silent")
	}
	// ch7/ch8 must be silent: a loopback that is never audible is what keeps
	// the AEC off the hardware reference on this board.
	if donutSlot[7] != -1 || donutSlot[8] != -1 {
		t.Error("ch7/ch8 must stay silent — this capture has no loopback")
	}
}
