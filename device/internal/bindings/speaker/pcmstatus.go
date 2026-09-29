package speaker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wilbowes/EchoMuse/pkg/board"
)

// The speaker is card 0 device 23; the mic is device 24. Here rather than in
// pcm_speaker.go because that file is ARM-only (build tag `server`) and the
// status-path test needs to pin these on the host.
const cardNr = 0
const deviceNr = 23

// On the Echo Dot 3rd gen the speaker is DL1_Playback, device 6 (and the mic
// TDM_Capture, device 1).
const donutDeviceNr = 6

// playbackDevice is the speaker's PCM device on a board. Anything not
// positively the Dot 3 keeps biscuit's, as every build did before.
func playbackDevice(b *board.Board) int {
	if b == board.Donut {
		return donutDeviceNr
	}
	return deviceNr
}

// dacUnity is the DAC digital volume's 0dB index. The DAC stays here while
// audio is live and the user's volume is applied to the PCM (swvolume.go);
// Init and Close still use the control to mute around amp and stream
// changes. Here rather than in pcm_speaker.go so the host test can pin it;
// the Echo Dot 3rd gen's is different — see unityVolume.
const dacUnity = "127"

// unityVolume is the "PCM Playback Volume" value for 0dB, where the hardware
// sits while the user's volume is applied in software (swvolume.go).
//
// On biscuit that control is the DAC's digital volume, 0..127 with 127 = 0dB.
// On the Echo Dot 3rd gen the same NAME is the TAS2770 amp's digital volume,
// INVERTED and wider: 0..255 with 255 = 0dB, -0.5dB a step, 0 = mute
// (measured 2026-09-28: 255 reads back as amp register 0x05 = 0x00). So
// biscuit's 127 there is -64dB, which is near silence at every volume.
func unityVolume(b *board.Board) string {
	if b == board.Donut {
		return "255"
	}
	return dacUnity
}

// The playback substream's status file, which is how we find out whether
// anyone else holds the speaker BEFORE trying to open it.
//
// This exists because tinyalsa's pcm_open is open(fn, O_RDWR) with no
// O_NONBLOCK, and we link -ltinyalsa from the compiler image's sysroot rather
// than vendoring its source — so the open itself cannot be made non-blocking
// without patching the toolchain's library. ALSA core parks a blocking open on
// pcm->open_wait for as long as the substream is busy, with no timeout, which
// on this hardware means forever.
//
// Measured on a stranded device (issue #80, 2026-08-09): a Dot booted with a
// headphone plug inserted has Android's mediaserver holding this substream,
// and our thread sat in snd_pcm_open for eighteen minutes with the whole of
// main() behind it — no buttons, no wake word, no controller registration.
func statusPath(card, device int) string {
	return fmt.Sprintf("/proc/asound/card%d/pcm%dp/sub0/status", card, device)
}

// pcmFree reports whether the substream is available to open.
//
// A free substream's status file contains exactly "closed"; a held one leads
// with "state: <STATE>" and an owner_pid. Anything we do not recognise counts
// as FREE, deliberately: this guard exists to avoid a hang we know how to
// detect, and treating an unfamiliar format as busy would refuse to open a
// perfectly good speaker on some device whose procfs we have never seen.
// Failing open costs us the old behaviour; failing closed costs the speaker.
func pcmFree(status string) bool {
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(status), "\n", 2)[0])
	if strings.EqualFold(first, "closed") {
		return true
	}
	// Busy has one shape and we match it POSITIVELY: a held substream leads
	// with "state: <STATE>". Treating everything-that-is-not-closed as busy
	// would make an unfamiliar procfs format indistinguishable from a device
	// we cannot have, and refuse to open a speaker that was never held.
	key, _, found := strings.Cut(first, ":")
	return !(found && strings.EqualFold(strings.TrimSpace(key), "state"))
}

// pcmOwner returns the pid holding the substream, or 0 if the status does not
// name one. Reported in the log line so a stall names the culprit — the whole
// diagnosis of #80 turned on finding "owner_pid: 659" in this file, and that
// took a day of hardware round trips to reach.
func pcmOwner(status string) int {
	for _, line := range strings.Split(status, "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "owner_pid" {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return n
		}
	}
	return 0
}
