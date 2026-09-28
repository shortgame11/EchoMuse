package mic

import "math"

// High-pass filter on the Dot 3's capture, before anything else reads it.
//
// Why: the Dot 3's mics carry mains hum, and it is most of their noise.
// Measured 2026-09-28 on channel 3 (the wake word's input), first 3s of an
// idle room, USB unplugged: -62.3 dBFS total, of which -63.1 below 300 Hz and
// -63.4 in the 100 Hz bin alone, against -70.2 for 300 Hz-1 kHz and -83.4 for
// 1-3.5 kHz. With a USB cable to a laptop on its charger it was -51.7 total,
// still led by 100 Hz. Speech sits at about -45 dBFS, so the hum was eating
// most of the wake word's margin, and none of it is speech: the wake model's
// mel front end and the ASR both start above it.
//
// 4th-order Butterworth at 150 Hz, as two cascaded biquads (RBJ cookbook,
// bilinear transform). Analogue response at 16 kHz, which the bilinear warp
// barely moves at these frequencies: 100 Hz -14.3 dB, 200 Hz -0.4 dB, 300 Hz
// -0.02 dB. A 2nd-order section alone gives only -8 dB at 100 Hz, and a
// higher corner starts cutting voiced speech's fundamental, so this is the
// order and corner that remove the hum and leave the voice.
//
// Dot 3 only. biscuit's noise floor was never measured to need it, and
// changing the input to a working wake word on the whole fleet wants its own
// measurement first.
//
// Float64, state per channel and stage, carried across reads — a filter that
// restarted per read would click every 160ms. Output is rounded and clamped
// to 24 bits, since a high-pass overshoots on a full-scale step and a wrapped
// sample is a full-scale click of the opposite sign.

const (
	hpCornerHz   = 150
	hpSampleRate = 16000
	hpStages     = 2
)

// butterworthQ4 are the Q values of the two second-order sections of a
// 4th-order Butterworth: 1/(2cos(π/8)) and 1/(2cos(3π/8)).
var butterworthQ4 = [hpStages]float64{
	1 / (2 * math.Cos(math.Pi/8)),
	1 / (2 * math.Cos(3*math.Pi/8)),
}

type biquad struct{ b0, b1, b2, a1, a2 float64 }

func highpassSection(fc, fs, q float64) biquad {
	w0 := 2 * math.Pi * fc / fs
	cw, alpha := math.Cos(w0), math.Sin(w0)/(2*q)
	a0 := 1 + alpha
	return biquad{
		b0: (1 + cw) / 2 / a0,
		b1: -(1 + cw) / a0,
		b2: (1 + cw) / 2 / a0,
		a1: -2 * cw / a0,
		a2: (1 - alpha) / a0,
	}
}

// highpass filters the Dot 3's four channels independently.
type highpass struct {
	stages [hpStages]biquad
	z      [donutChannels][hpStages][2]float64 // transposed direct form II state
}

func newDonutHighpass() *highpass {
	h := &highpass{}
	for i, q := range butterworthQ4 {
		h.stages[i] = highpassSection(hpCornerHz, hpSampleRate, q)
	}
	return h
}

const (
	s24Max = 1<<23 - 1
	s24Min = -1 << 23
)

// sample filters one 24-bit sample of channel ch.
func (h *highpass) sample(ch int, x int32) int32 {
	v := float64(x)
	for s := range h.stages {
		c, z := &h.stages[s], &h.z[ch][s]
		y := c.b0*v + z[0]
		z[0] = c.b1*v - c.a1*y + z[1]
		z[1] = c.b2*v - c.a2*y
		// A muted ADC delivers exact zeros; left alone the state decays into
		// subnormals, which some FPUs handle in slow microcode. Nothing this
		// small is audible in a 24-bit sample.
		if math.Abs(z[0]) < 1e-20 {
			z[0] = 0
		}
		if math.Abs(z[1]) < 1e-20 {
			z[1] = 0
		}
		v = y
	}
	switch {
	case v >= s24Max:
		return s24Max
	case v <= s24Min:
		return s24Min
	}
	return int32(math.Round(v))
}
