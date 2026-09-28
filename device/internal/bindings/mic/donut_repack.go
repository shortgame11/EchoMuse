package mic

// Echo Dot 3rd gen capture, reshaped into biscuit's layout.
//
// Everything downstream of the mic — the beamformer, the AEC's hardware
// reference, the wake word, the data plane — was written against biscuit's
// capture: 9 channels of S24_3LE, the perimeter mics on ch0-5, the centre mic
// on ch6 and the playback loopback on ch7/ch8. The Dot 3 captures 4 channels
// of S32_LE (two tlv320aic3101 ADCs on TDM_Capture). Rather than teach every
// consumer a second layout, the capture is rewritten into the first one here,
// so the rest of the pipeline is the same code on both boards.
//
// FRAME FOR FRAME, one output batch per read. GoTinyAlsa reads the WHOLE ALSA
// buffer per call (FrameBytesSize is pcm_get_buffer_size), so biscuit's
// batches are 5 x 512 = 2560 frames, 160ms — readLoop's ledger comment is
// about exactly those 160ms steps — and everything downstream is paced by
// that. The first version of this re-batched each read into five 512-frame
// batches handed over back to back: readLoop then logged a "capture stall"
// six times a second (a 32ms batch 160ms after the last one) and the AEC
// resynced its reference on every burst. Measured on the Dot 3 2026-09-28:
// stalls=1636 and resyncs=1160 inside an hour, with no audio actually lost.
//
// Precision: the S32 samples are 24-bit data left-justified, so keeping the
// top three bytes loses nothing — the same 24 bits biscuit delivers, and the
// same gain arithmetic in beamformer.extractChannel.
//
// Channel placement. Nothing has measured the Dot 3's mic geometry yet. The
// firmware that first ran on it treated ch3 as the centre (omni) mic, which is
// what listens for the wake word, and ch0-2 as a triangle at 0/120/240°; that
// is kept, because it is what worked. Each of biscuit's six perimeter
// directions gets the NEAREST real mic, rather than silence, so a fixed beam
// angle can never steer onto a dead channel. ch7/ch8 stay zero: this capture
// carries no playback loopback, and a reference that is silent even while the
// speaker plays is never confirmed as one (client.noteEchoRef), so the AEC
// stays on the software tap by itself.

const (
	donutChannels   = 4
	donutSampleSize = 4 // S32_LE
	donutFrameBytes = donutChannels * donutSampleSize

	outChannels   = 9
	outSampleSize = 3 // S24_3LE
	outFrameBytes = outChannels * outSampleSize
)

// donutSlot maps each of biscuit's 9 channels to the Dot 3 channel that fills
// it, or -1 for silence. Perimeter slots are biscuit's 330/30/90/150/210/270°.
var donutSlot = [outChannels]int{
	0,      // ch0 330° ← Dot 3 ch0 (0°)
	0,      // ch1  30° ← Dot 3 ch0 (0°)
	1,      // ch2  90° ← Dot 3 ch1 (120°)
	1,      // ch3 150° ← Dot 3 ch1 (120°)
	2,      // ch4 210° ← Dot 3 ch2 (240°)
	2,      // ch5 270° ← Dot 3 ch2 (240°)
	3,      // ch6 centre ← Dot 3 ch3
	-1, -1, // ch7/ch8: no loopback in this capture
}

// repacker turns each Dot 3 read into one biscuit-shaped batch of the same
// number of frames. Owned by the read loop alone.
type repacker struct {
	tail []byte // a partial input frame carried to the next push
}

// push converts every whole frame in in (after any carried partial frame) and
// calls emit ONCE with the result, or not at all if no frame completed. The
// emitted slice is newly allocated and never touched again, because
// subscribers keep it (the mic fans the same slice out to several of them,
// see readLoop). A read is always whole frames in practice; the tail exists so
// a short read can never shift every later sample into the wrong channel.
func (r *repacker) push(in []byte, emit func([]byte)) {
	var head []byte
	if len(r.tail) > 0 {
		need := donutFrameBytes - len(r.tail)
		if len(in) < need {
			r.tail = append(r.tail, in...)
			return
		}
		head = append(r.tail, in[:need]...)
		r.tail = nil
		in = in[need:]
	}
	whole := len(in) / donutFrameBytes
	n := whole
	if head != nil {
		n++
	}
	if rest := in[whole*donutFrameBytes:]; len(rest) > 0 {
		r.tail = append([]byte(nil), rest...)
	}
	if n == 0 {
		return
	}
	out := make([]byte, n*outFrameBytes)
	o := 0
	if head != nil {
		frame(out[:outFrameBytes], head)
		o = outFrameBytes
	}
	for f := 0; f < whole; f++ {
		frame(out[o+f*outFrameBytes:o+(f+1)*outFrameBytes], in[f*donutFrameBytes:(f+1)*donutFrameBytes])
	}
	emit(out)
}

// frame converts one Dot 3 frame into one biscuit frame.
func frame(dst, src []byte) {
	for slot, ch := range donutSlot {
		o := slot * outSampleSize
		if ch < 0 {
			dst[o], dst[o+1], dst[o+2] = 0, 0, 0
			continue
		}
		i := ch*donutSampleSize + 1 // drop the low byte: S32 → S24
		dst[o], dst[o+1], dst[o+2] = src[i], src[i+1], src[i+2]
	}
}
