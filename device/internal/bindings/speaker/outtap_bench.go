//go:build server && bench

package speaker

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

// Bench-only capture of the speaker output exactly as the write loop hands it
// to ALSA — after the mix, the output chain, the volume and any cue — as a
// WAV. Record it at the same moment as the Dot 3's DL1 loopback
// (mic/awbtap_bench.go): a glitch in both was already in the audio; a glitch
// only in the loopback happened between the write and the DAC.
//
//	echo 8 > /data/local/tmp/em-outcap-request
//
// writes /data/local/tmp/outcap-<unix>.wav for that many seconds (max 60),
// 2 ch S16 48 kHz. The write loop never waits on the disk: periods are
// dropped, and counted, if the writer falls behind.

const (
	outcapRequest = "/data/local/tmp/em-outcap-request"
	outcapDir     = "/data/local/tmp"
	outcapMaxSecs = 60
	outcapRate    = 48000
	outcapChans   = 2
)

func init() {
	ch := make(chan []byte, 64)
	var active atomic.Bool
	var dropped atomic.Int64
	outTap = func(b []byte) {
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
			raw, err := os.ReadFile(outcapRequest)
			if err != nil {
				continue
			}
			os.Remove(outcapRequest)
			secs, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil || secs < 1 || secs > outcapMaxSecs {
				log.Printf("[outcap] bad request %q (1-%d seconds)", raw, outcapMaxSecs)
				continue
			}
			path := fmt.Sprintf("%s/outcap-%d.wav", outcapDir, time.Now().Unix())
			f, err := os.Create(path)
			if err != nil {
				log.Printf("[outcap] %v", err)
				continue
			}
			f.Write(make([]byte, 44)) // header written at the end, once the size is known
			log.Printf("[outcap] recording %ds to %s", secs, path)
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
			f.WriteAt(outWavHeader(uint32(written)), 0)
			f.Close()
			log.Printf("[outcap] done: %s, %d bytes (%.1fs), %d batches dropped",
				path, written, float64(written)/(outcapRate*outcapChans*2), dropped.Load())
		}
	}()
}

// wavHeader is a canonical 44-byte PCM header for 2 ch S16 48 kHz.
func outWavHeader(dataBytes uint32) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+dataBytes)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], outcapChans)
	binary.LittleEndian.PutUint32(h[24:], outcapRate)
	binary.LittleEndian.PutUint32(h[28:], outcapRate*outcapChans*2)
	binary.LittleEndian.PutUint16(h[32:], outcapChans*2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataBytes)
	return h
}
