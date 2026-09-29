package speaker

import (
	"testing"

	"github.com/wilbowes/EchoMuse/pkg/board"
)

// Verbatim from Test Device (G090LF1180570SPJ) 2026-08-09, while Android's
// mediaserver held the speaker and the server was stranded in snd_pcm_open.
const heldStatus = `state: PREPARED
owner_pid   : 659
trigger_time: 0.000000000
tstamp      : 1239.509457377
delay       : 0
avail       : 3072
avail_max   : 0
-----
hw_ptr      : 0
appl_ptr    : 0
`

// The same file once our own thread had taken the device over.
const runningStatus = `state: RUNNING
owner_pid   : 1094
trigger_time: 1786260229.525790163
tstamp      : 1786260262.082172242
delay       : 8064
avail       : 128
avail_max   : 6256
-----
hw_ptr      : 1562816
appl_ptr    : 1570880
`

func TestPcmFreeWhenClosed(t *testing.T) {
	if !pcmFree("closed\n") {
		t.Fatal("a closed substream must read as free")
	}
}

func TestPcmBusyWhenHeld(t *testing.T) {
	if pcmFree(heldStatus) {
		t.Fatal("a PREPARED substream held by another process must read as busy")
	}
	if pcmFree(runningStatus) {
		t.Fatal("a RUNNING substream must read as busy")
	}
}

// Unknown content must not block the open. Refusing on an unrecognised format
// would disable the speaker on hardware whose procfs we have never seen, to
// avoid a hang that device may not even have.
func TestUnknownStatusFailsOpen(t *testing.T) {
	for _, s := range []string{"", "   ", "something we have never seen"} {
		if !pcmFree(s) {
			t.Fatalf("unrecognised status %q must read as free, not busy", s)
		}
	}
}

func TestPcmOwner(t *testing.T) {
	if got := pcmOwner(heldStatus); got != 659 {
		t.Fatalf("owner_pid = %d, want 659", got)
	}
	if got := pcmOwner(runningStatus); got != 1094 {
		t.Fatalf("owner_pid = %d, want 1094", got)
	}
	if got := pcmOwner("closed\n"); got != 0 {
		t.Fatalf("a closed substream names no owner, got %d", got)
	}
}

func TestStatusPathMatchesTheDeviceWeOpen(t *testing.T) {
	// Pins the path against the card/device constants. The speaker is device
	// 23 and the mic is 24; checking pcm0p (Android's own) reads "closed" and
	// proves nothing, which cost a wrong conclusion during the #80 hunt.
	want := "/proc/asound/card0/pcm23p/sub0/status"
	if got := statusPath(cardNr, deviceNr); got != want {
		t.Fatalf("statusPath = %q, want %q", got, want)
	}
}

func TestEachBoardOpensItsOwnSpeaker(t *testing.T) {
	for _, c := range []struct {
		b      *board.Board
		dev    int
		unity  string
		status string
	}{
		{nil, 23, "127", "/proc/asound/card0/pcm23p/sub0/status"},
		{board.Biscuit, 23, "127", "/proc/asound/card0/pcm23p/sub0/status"},
		{board.Donut, 6, "255", "/proc/asound/card0/pcm6p/sub0/status"},
	} {
		id := board.IDOf(c.b)
		if got := playbackDevice(c.b); got != c.dev {
			t.Errorf("%s: playback device %d, want %d", id, got, c.dev)
		}
		if got := statusPath(cardNr, playbackDevice(c.b)); got != c.status {
			t.Errorf("%s: status path %q, want %q", id, got, c.status)
		}
		if got := unityVolume(c.b); got != c.unity {
			t.Errorf("%s: unity volume %q, want %q", id, got, c.unity)
		}
	}
}
