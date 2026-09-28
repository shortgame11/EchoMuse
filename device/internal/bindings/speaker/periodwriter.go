package speaker

// periodWriter hands audio to ALSA in exact hardware periods, carrying any
// remainder to the next call.
//
// The write loop produces 2048-frame mixed periods and used to write them as
// 1024-frame pieces. On biscuit the hardware period IS 1024 frames, so every
// write ended on a period boundary. On the Echo Dot 3rd gen the DL1 period is
// 768 frames, so almost every write ended mid-period — and MediaTek's DL1
// then skipped or replayed audio: recorded on 2026-09-28, the output as
// handed to ALSA was clean (0 glitches in 8s of a 1 kHz tone) while the DL1
// loopback of the same moment had 12, each a phase jump of ±16 samples mod
// 48 (1024 = 16 mod 48) with no change in level and no silence. tinyplay,
// which writes one period at a time, played the same tone cleanly.
//
// When the chunk divides the mixed period there is never a remainder, so
// biscuit's writes are exactly what they were: same sizes, no copy.
type periodWriter struct {
	chunk   int    // bytes per hardware period
	pending []byte // < chunk bytes carried from the last call
	buf     []byte // scratch for joining pending with new audio
}

func newPeriodWriter(chunkBytes int) *periodWriter {
	return &periodWriter{chunk: chunkBytes}
}

// write passes every whole period in pending+out to w, in order, and keeps
// the rest. It never calls w with anything but exactly one period.
func (pw *periodWriter) write(out []byte, w func([]byte) error) error {
	data := out
	if len(pw.pending) > 0 {
		pw.buf = append(append(pw.buf[:0], pw.pending...), out...)
		data = pw.buf
	}
	n := len(data) / pw.chunk * pw.chunk
	for off := 0; off < n; off += pw.chunk {
		if err := w(data[off : off+pw.chunk]); err != nil {
			return err
		}
	}
	pw.pending = append(pw.pending[:0], data[n:]...)
	return nil
}
