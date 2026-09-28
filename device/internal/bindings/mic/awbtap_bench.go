//go:build server && bench

package mic

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Bench-only speaker-loopback capture (Echo Dot 3rd gen): what DL1 actually
// played, as a WAV, read from the clock anchor's DL1_AWB_Record stream — which
// EchoMuse already holds open, so nothing else has to open a capture device.
// (Opening the card's OTHER loopback, AWB_Record on pcm3c, while DL1 was
// playing rebooted a Dot 3 on 2026-09-28. Do not do that.)
//
// Written to find where playback clicks enter: a glitch in this recording is
// in the audio data; a clean recording with an audible click means the fault
// is after the DAC input.
//
// Same shape as rawtap_bench.go, triggered by a file:
//
//	echo 15 > /data/local/tmp/em-awbcap-request
//
// writes /data/local/tmp/awbcap-<unix>.wav for that many seconds (max 60).
// The anchor goroutine never waits on the disk: batches are dropped, and
// counted, if the writer falls behind.

const (
	awbcapRequest = "/data/local/tmp/em-awbcap-request"
	awbcapDir     = "/data/local/tmp"
	awbcapMaxSecs = 60
	awbcapRate    = 48000
	awbcapChans   = 2
)

func init() {
	ch := make(chan []byte, 64)
	var active atomic.Bool
	var dropped atomic.Int64
	awbTap = func(b []byte) {
		if !active.Load() {
			return
		}
		select {
		case ch <- b:
		default:
			dropped.Add(1)
		}
	}
	go func() {
		for {
			time.Sleep(500 * time.Millisecond)
			raw, err := os.ReadFile(awbcapRequest)
			if err != nil {
				continue
			}
			os.Remove(awbcapRequest)
			secs, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil || secs < 1 || secs > awbcapMaxSecs {
				log.Printf("[awbcap] bad request %q (1-%d seconds)", raw, awbcapMaxSecs)
				continue
			}
			path := fmt.Sprintf("%s/awbcap-%d.wav", awbcapDir, time.Now().Unix())
			f, err := os.Create(path)
			if err != nil {
				log.Printf("[awbcap] %v", err)
				continue
			}
			f.Write(make([]byte, 44)) // header written at the end, once the size is known
			log.Printf("[awbcap] recording %ds to %s", secs, path)
			dropped.Store(0)
			active.Store(true)
			var written int64
			deadline := time.After(time.Duration(secs) * time.Second)
		loop:
			for {
				select {
				case b := <-ch:
					n, _ := f.Write(b)
					written += int64(n)
				case <-deadline:
					break loop
				}
			}
			active.Store(false)
			for len(ch) > 0 {
				n, _ := f.Write(<-ch)
				written += int64(n)
			}
			f.WriteAt(wavHeader(uint32(written)), 0)
			f.Close()
			log.Printf("[awbcap] done: %s, %d bytes (%.1fs), %d batches dropped",
				path, written, float64(written)/(awbcapRate*awbcapChans*2), dropped.Load())
		}
	}()
}

// wavHeader is a canonical 44-byte PCM header for 2 ch S16 48 kHz.
func wavHeader(dataBytes uint32) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+dataBytes)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], awbcapChans)
	binary.LittleEndian.PutUint32(h[24:], awbcapRate)
	binary.LittleEndian.PutUint32(h[28:], awbcapRate*awbcapChans*2)
	binary.LittleEndian.PutUint16(h[32:], awbcapChans*2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataBytes)
	return h
}
