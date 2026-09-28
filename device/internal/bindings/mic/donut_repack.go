package mic

// Echo Dot 3rd gen capture, reshaped into biscuit's layout.
//
// Everything downstream of the mic — the beamformer, the AEC's hardware
// reference, the wake word, the data plane — was written against biscuit's
// capture: 9 channels of S24_3LE, 512 frames a batch, the perimeter mics on
// ch0-5, the centre mic on ch6 and the playback loopback on ch7/ch8. The Dot 3
// captures 4 channels of S32_LE (two tlv320aic3101 ADCs on TDM_Capture) and
// its driver was run at 256-frame periods. Rather than teach every consumer a
// second layout, the capture is rewritten into the first one here, so the rest
// of the pipeline is the same code on both boards.
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
	outFrames     = 512 // biscuit's period, which the beamformer indexes by
	outBatchBytes = outFrames * outFrameBytes
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

// repacker turns Dot 3 capture periods into biscuit-shaped batches.
//
// Input arrives in whatever period the driver runs; output is always exactly
// outFrames frames, emitted as each batch fills. Owned by the read loop alone.
type repacker struct {
	batch []byte // the batch being filled; a fresh slice every batch
	n     int    // frames written into batch
	tail  []byte // a partial input frame carried to the next push
}

// push converts in and calls emit once per completed batch. Each emitted slice
// is newly allocated and never touched again, because subscribers keep it
// (the mic fans the same slice out to several of them, see readLoop).
func (r *repacker) push(in []byte, emit func([]byte)) {
	if len(r.tail) > 0 {
		need := donutFrameBytes - len(r.tail)
		if len(in) < need {
			r.tail = append(r.tail, in...)
			return
		}
		frame := append(r.tail, in[:need]...)
		r.tail = r.tail[:0]
		in = in[need:]
		r.frame(frame, emit)
	}
	for len(in) >= donutFrameBytes {
		r.frame(in[:donutFrameBytes], emit)
		in = in[donutFrameBytes:]
	}
	if len(in) > 0 {
		r.tail = append(r.tail[:0], in...)
	}
}

// frame writes one input frame into the current batch.
func (r *repacker) frame(src []byte, emit func([]byte)) {
	if r.batch == nil {
		r.batch = make([]byte, outBatchBytes)
	}
	dst := r.batch[r.n*outFrameBytes : (r.n+1)*outFrameBytes]
	for slot, ch := range donutSlot {
		o := slot * outSampleSize
		if ch < 0 {
			dst[o], dst[o+1], dst[o+2] = 0, 0, 0
			continue
		}
		i := ch*donutSampleSize + 1 // drop the low byte: S32 → S24
		dst[o], dst[o+1], dst[o+2] = src[i], src[i+1], src[i+2]
	}
	r.n++
	if r.n == outFrames {
		emit(r.batch)
		r.batch, r.n = nil, 0
	}
}
