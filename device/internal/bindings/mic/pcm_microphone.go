//go:build server

package mic

import (
	"context"
	"errors"
	"log"
	"os/exec"
	"sync"
	"time"
	"os"
	"strings"

	"github.com/wilbowes/EchoMuse/internal/bindings/codec"
	pkgmic "github.com/wilbowes/EchoMuse/pkg/mic"
	"github.com/Binozo/GoTinyAlsa/pkg/pcm"
	"github.com/Binozo/GoTinyAlsa/pkg/tinyalsa"
)

const cardNr = 0
const deviceNr = 1

// rawTap receives every raw 9-channel batch, and is nil in release builds.
// Only rawtap_bench.go sets it (build tag bench): it records the mics to
// disk, which release firmware must be unable to do.
var rawTap func([]byte)

// PcmMicrophone opens the ALSA device once and fans out to multiple subscribers.
// Callers register via Listen(); each gets their own buffered channel.
type PcmMicrophone struct {
	device *tinyalsa.AlsaDevice
	mu     sync.Mutex
	subs   []chan []byte
}

// clockAnchor holds DL1_AWB_Record (the speaker loopback, 48 kHz) open for the
// life of the process, and is started BEFORE the mic.
//
// Echo Dot 3rd gen (donut_puffin): the TDM mic capture and the I2S port that
// feeds the TAS2770 speaker amp share an audio clock. Opening the mic at
// 16 kHz with nothing else running configures that clock so the speaker port
// cannot produce a valid 48 kHz frame; the amp latches a TDM clock error
// (INT_LTCH0 bit 2) and shuts down, and it stays that way until a reboot.
// Amazon's mixer avoids it by opening this 48 kHz loopback first, from the
// same process, and so do we. Measured 2026-09-28: mic alone at 16 kHz breaks
// the speaker; loopback first, then mic, both work.
//
// Returns immediately on boards where device 7 is something else.
const awbDevice = 7

func startClockAnchor() {
	info, err := os.ReadFile("/proc/asound/card0/pcm7c/info")
	if err != nil || !strings.Contains(string(info), "DL1_AWB_Record") {
		return
	}
	dev := tinyalsa.NewDevice(cardNr, awbDevice, pcm.Config{
		Channels:         2,
		SampleRate:       48000,
		PeriodSize:       768,
		PeriodCount:      10,
		Format:           tinyalsa.PCM_FORMAT_S16_LE,
		StartThreshold:   768,
		StopThreshold:    7680,
		SilenceThreshold: 7680,
	})
	stream := make(chan []byte, 16)
	ready := make(chan struct{})
	go func() {
		if err := dev.GetAudioStream(dev.DeviceConfig, stream); err != nil {
			log.Printf("[mic] clock anchor (DL1_AWB_Record) stream error: %v", err)
		}
	}()
	go func() {
		first := true
		for range stream { // drain forever; an overrun would stop the stream
			if first {
				close(ready)
				first = false
			}
		}
		log.Printf("[mic] clock anchor stream closed — speaker may lose its clock")
	}()
	select {
	case <-ready:
		log.Printf("[mic] clock anchor running (DL1_AWB_Record 48 kHz) — mic may open")
	case <-time.After(2 * time.Second):
		log.Printf("[mic] clock anchor did not start within 2s — opening mic anyway")
	}
}

// NewMicrophone returns the pre-configured microphone alsa device and starts
// the permanent ALSA read loop.
func NewMicrophone() (*PcmMicrophone, error) {
	device := tinyalsa.NewDevice(cardNr, 1, pcm.Config{
        Channels:         4,
        SampleRate:       16000,
        PeriodSize:       256,
        PeriodCount:      10,  // To yield buffer_size 2560
        Format:           tinyalsa.PCM_FORMAT_S32_LE,
        StartThreshold:   256,
        StopThreshold:    2560,
        SilenceThreshold: 2560,
    })
	m := &PcmMicrophone{
		device: &device,
	}
	if err := m.Init(); err != nil {
		return nil, err
	}
	return m, nil
}

// Init stops the mixer service (required to release the ALSA capture device)
// then starts the permanent background ALSA read loop.
func (p *PcmMicrophone) Init() error {
	cmd := exec.Command("stop", "mixer")
	if err := cmd.Run(); err != nil {
		log.Printf("mic: stop mixer: %v (continuing)", err)
	}

	startClockAnchor()

	// Route the differential mic inputs into the ADCs before opening the PCM.
	// Without this the ADCs are powered down and capture returns the I2S bus's
	// own noise floor — with a perfectly healthy ALSA clock, which is what
	// makes it so hard to see. See the codec package.
	codec.EnsureRoutes()
	go p.readLoop()
	return nil
}

// readLoop opens the ALSA device and reads periods forever, fanning each
// period out to all current subscribers. Runs for the lifetime of the process.
// When the stream ends (ALSA error), all subscriber channels are closed so
// callers unblock and can detect the death rather than hanging on empty channels.
//
// Capture-loss telemetry (2026-07-10): the ALSA ring is only PeriodSize ×
// PeriodCount = 160ms deep, so any stall of this chain longer than that
// loses whole batches at the hardware with no error surfaced anywhere —
// discovered via the AEC reference governor tripping every ~20s on backlogs
// of exactly N×2560 samples. Two measurements below: per-batch arrival gaps
// (a gap ≫ the batch duration is an overrun in progress) and a ~1/min
// audio-vs-wall-clock ledger.
//
// THE LEDGER'S SIGN IS ITS WHOLE MEANING, and it reads both ways. Positive
// (wall ahead of audio) is audio the pipeline never got — overruns, the case
// this was built for. Negative is the capture clock running FAST, which is
// the ordinary state of this hardware and not a fault.
//
// Measured on SPJ over 11.8h, 2026-09-02: skew went -154ms to -14809ms, a
// steady **+345ppm** of the ALSA sample clock against CLOCK_MONOTONIC. The
// starting -154ms is just this loop counting the first batch's 160ms while
// wall starts at its arrival; everything after is the rate mismatch.
//
// It arrives in 160ms STEPS, roughly one every 7.7 minutes, and the earlier
// version of this comment said a rate mismatch would grow the deficit
// "smoothly rather than in stall-sized steps" — it does not, because
// delivery is quantised to whole 160ms batches, so the surplus banks in the
// ALSA ring until it is a whole batch and then lands at once. 160ms ÷
// 345ppm = 464s predicted against ~462s observed, which is what identifies
// it. So step-shaped growth does NOT distinguish overruns from drift; the
// SIGN does, and a device that is losing audio also raises stalls.
//
// Consequence for whoever reads a long uptime: seconds of accumulated
// negative skew are expected and mean nothing is wrong.
func (p *PcmMicrophone) readLoop() {
	stream := make(chan []byte, 16)

	go func() {
		if err := p.device.GetAudioStream(p.device.DeviceConfig, stream); err != nil {
			log.Printf("mic: ALSA stream error: %v", err)
		}
	}()

	rate := int64(p.device.DeviceConfig.SampleRate)
	bytesPerFrame := p.device.DeviceConfig.Channels * 4 // S24_3LE
	var (
		firstArrival time.Time
		lastArrival  time.Time
		lastReport   time.Time
		framesTotal  int64
		stalls       uint64
		subDrops     uint64
	)

	for audio := range stream {
		now := time.Now()
		frames := int64(len(audio) / bytesPerFrame)
		batchDur := time.Duration(frames) * time.Second / time.Duration(rate)
		if firstArrival.IsZero() {
			firstArrival, lastReport = now, now
		} else if gap := now.Sub(lastArrival); gap > 2*batchDur {
			stalls++
			log.Printf("[mic] capture stall: %dms between %dms batches — ~%dms lost to ALSA overrun (stalls=%d)",
				gap.Milliseconds(), batchDur.Milliseconds(),
				(gap - batchDur).Milliseconds(), stalls)
		}
		lastArrival = now
		framesTotal += frames
		if now.Sub(lastReport) >= time.Minute {
			wall := now.Sub(firstArrival)
			audioDur := time.Duration(framesTotal) * time.Second / time.Duration(rate)
			// Name the direction rather than leaving a signed number to be
			// read as loss either way — see readLoop's ledger note.
			skew := (wall - audioDur).Milliseconds()
			sense := "lost"
			if skew < 0 {
				sense = "capture fast"
			}
			log.Printf("[mic] clock: %.1fs audio over %.1fs wall (skew %+dms %s, stalls=%d, sub_drops=%d)",
				audioDur.Seconds(), wall.Seconds(), skew, sense, stalls, subDrops)
			lastReport = now
		}

		// GetAudioStream hands over a fresh slice per read (GoTinyAlsa #1),
		// so this can be passed on as-is. Copying here was too late: the
		// library reused one buffer, and a batch still queued in stream was
		// overwritten by the next read — repeated or torn audio (#607).
		buf := audio
		if rawTap != nil {
			rawTap(buf)
		}

		p.mu.Lock()
		for _, ch := range p.subs {
			select {
			case ch <- buf:
			default:
				// Subscriber too slow — drop this period rather than block
				subDrops++
				if subDrops == 1 || subDrops%64 == 0 {
					log.Printf("[mic] subscriber channel full — batch dropped (sub_drops=%d)", subDrops)
				}
			}
		}
		p.mu.Unlock()
	}

	// Stream ended — close all subscriber channels so callers see EOF rather
	// than blocking on a channel that will never receive again.
	p.mu.Lock()
	log.Printf("mic: ALSA stream closed — notifying %d subscribers", len(p.subs))
	for _, ch := range p.subs {
		close(ch)
	}
	p.subs = nil
	p.mu.Unlock()
}

// subscribe registers a new subscriber and returns its channel.
func (p *PcmMicrophone) Subscribe() chan []byte {
	ch := make(chan []byte, 32)
	p.mu.Lock()
	p.subs = append(p.subs, ch)
	p.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel. Safe to call even if readLoop has
// already closed the channel (e.g. after an ALSA stream error).
func (p *PcmMicrophone) Unsubscribe(ch chan []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.subs {
		if s == ch {
			p.subs = append(p.subs[:i], p.subs[i+1:]...)
			// Only close if readLoop hasn't already closed it (subs==nil means
			// readLoop ran the close-all path and cleared the slice).
			// We detect this by the channel still being in the slice — if we
			// found it, readLoop hasn't closed it yet.
			close(ch)
			return
		}
	}
	// Not found — readLoop already closed and cleared it. Nothing to do.
}

// Listen subscribes to the permanent mic stream and calls callback for each
// period until ctx is cancelled. Satisfies the pkgmic.Microphone interface.
func (p *PcmMicrophone) Listen(callback pkgmic.AudioCallback, ctx context.Context) error {
	if callback == nil {
		return errors.New("callback can't be nil")
	}
	ch := p.Subscribe()
	defer p.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return nil
		case audio, ok := <-ch:
			if !ok {
				return nil
			}
			callback(audio)
		}
	}
}
