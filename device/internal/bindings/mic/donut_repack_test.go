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

func TestRepackIsOneBatchPerRead(t *testing.T) {
	// GoTinyAlsa reads the whole buffer per call: 256 x 10 = 2560 frames on
	// the Dot 3. That must come out as ONE 2560-frame batch, the shape biscuit
	// delivers, and never as a burst of smaller ones.
	const frames = 2560
	in := donutCapture(frames, value)

	var r repacker
	var got [][]byte
	r.push(in, func(b []byte) { got = append(got, b) })
	if len(got) != 1 {
		t.Fatalf("%d batches from one read, want 1", len(got))
	}
	b := got[0]
	if len(b) != frames*outFrameBytes {
		t.Fatalf("batch is %d bytes, want %d", len(b), frames*outFrameBytes)
	}
	for f := 0; f < frames; f++ {
		for slot, ch := range donutSlot {
			o := f*outFrameBytes + slot*outSampleSize
			v := s24(b[o : o+3])
			want := int32(0)
			if ch >= 0 {
				want = value(f, ch)
			}
			if v != want {
				t.Fatalf("frame %d slot %d = %d, want %d", f, slot, v, want)
			}
		}
	}

	// A second read gets a fresh buffer: subscribers keep the first.
	r.push(in, func(b []byte) { got = append(got, b) })
	if &got[0][0] == &got[1][0] {
		t.Fatal("batches share a buffer; subscribers would see it overwritten")
	}
}

func TestRepackCarriesPartialFrames(t *testing.T) {
	// However the input is split, down to single bytes, the concatenated
	// output must equal converting it whole, with no frame lost or shifted.
	in := donutCapture(64, value)
	var whole, pieces []byte
	var a, b repacker
	a.push(in, func(x []byte) { whole = append(whole, x...) })
	for _, step := range []int{1, 7, 16, 23} {
		pieces = pieces[:0]
		b = repacker{}
		for i := 0; i < len(in); i += step {
			j := i + step
			if j > len(in) {
				j = len(in)
			}
			b.push(in[i:j], func(x []byte) { pieces = append(pieces, x...) })
		}
		if string(whole) != string(pieces) {
			t.Fatalf("split into %d-byte pieces: output differs from a whole read", step)
		}
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
