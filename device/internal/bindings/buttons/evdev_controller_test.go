package buttons

import (
	"testing"

	"github.com/wilbowes/EchoMuse/pkg/board"
	"github.com/wilbowes/EchoMuse/pkg/buttons"
)

func TestEachBoardReadsItsOwnInputDevices(t *testing.T) {
	for _, c := range []struct {
		b    *board.Board
		want []string
	}{
		{nil, []string{"/dev/input/event1", "/dev/input/event2"}},
		{board.Biscuit, []string{"/dev/input/event1", "/dev/input/event2"}},
		{board.Donut, []string{"/dev/input/event3", "/dev/input/event1"}},
	} {
		got := buttonDevices(c.b)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %v, want %v", board.IDOf(c.b), got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v, want %v", board.IDOf(c.b), got, c.want)
			}
		}
	}
}

func TestAKeyMeansTheSameOnEitherDevice(t *testing.T) {
	// biscuit's split — volume keys on one device, action and mute on the
	// other — is what the rest of the firmware sees, whichever device the Dot
	// 3 delivers them on.
	for code, want := range map[buttons.ClickType]buttons.ButtonType{
		buttons.VolumeUpClick:   buttons.VolumeButton,
		buttons.VolumeDownClick: buttons.VolumeButton,
		buttons.DotClick:        buttons.DotButton,
		buttons.MuteClick:       buttons.DotButton,
	} {
		if got := buttonTypeOf(code); got != want {
			t.Errorf("code %d: %s, want %s", code, got, want)
		}
	}
}
