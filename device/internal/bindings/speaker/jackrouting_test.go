package speaker

import (
	"testing"

	"github.com/wilbowes/EchoMuse/internal/bindings/mixer"
	"github.com/wilbowes/EchoMuse/pkg/board"
)

// The values here are measurements off a stock FireOS Dot, not preferences.
// Pinning them means a later edit has to disagree with the hardware on
// purpose rather than by accident.

func TestJackRoutingMutesInternalDriverWhenSomethingIsPluggedIn(t *testing.T) {
	got := jackRouting(true)
	if len(got) != 2 {
		t.Fatalf("want 2 writes, got %d: %+v", len(got), got)
	}
	if got[0].Ctl != ctlSpeakerAmp || got[0].Args[0] != "Off" {
		t.Errorf("internal amp must be Off with a plug in, got ctl %s = %v", got[0].Ctl, got[0].Args)
	}
}

func TestJackRoutingEnablesInternalDriverWhenNothingIsPluggedIn(t *testing.T) {
	got := jackRouting(false)
	if got[0].Ctl != ctlSpeakerAmp || got[0].Args[0] != "On" {
		t.Errorf("internal amp must be On with the jack empty, got ctl %s = %v", got[0].Ctl, got[0].Args)
	}
}

// The regression this whole change exists for: accdet leaves the jack's
// output stage at the floor of its range on insert, and nothing of ours used
// to raise it. A gain that is not set, or set to 0, is silence.
func TestJackRoutingRaisesTheJackOutputStageOnInsert(t *testing.T) {
	in := jackRouting(true)
	out := jackRouting(false)

	find := func(ws []mixerWrite) *mixerWrite {
		for i := range ws {
			if ws[i].Ctl == ctlHPDriverGain {
				return &ws[i]
			}
		}
		return nil
	}

	gi, go_ := find(in), find(out)
	if gi == nil || go_ == nil {
		t.Fatal("HP driver gain must be set for BOTH positions — leaving it unset on either side is how the jack went silent")
	}
	if gi.Args[0] != hpGainJack {
		t.Errorf("plug in: want gain %s (stock's value), got %s", hpGainJack, gi.Args[0])
	}
	if go_.Args[0] != hpGainInternal {
		t.Errorf("plug out: want gain %s restored, got %s", hpGainInternal, go_.Args[0])
	}
	if gi.Args[0] == "0" {
		t.Error("gain 0 is the FLOOR of the 0..35 range, which is the bug, not a setting")
	}
}

// Both channels take the same value: the control is a pair (L, R) and
// tinymix writes it as two arguments. One argument sets only the left.
func TestJackRoutingWritesBothChannelsOfTheGainPair(t *testing.T) {
	for _, inserted := range []bool{true, false} {
		for _, w := range jackRouting(inserted) {
			if w.Ctl != ctlHPDriverGain {
				continue
			}
			if len(w.Args) != 2 || w.Args[0] != w.Args[1] {
				t.Errorf("inserted=%v: gain needs two equal args, got %v", inserted, w.Args)
			}
		}
	}
}

// Watch dispatches the state it booted into, so every write runs twice in a
// row on an ordinary device. Nothing here may depend on the previous state.
func TestJackRoutingIsIdempotent(t *testing.T) {
	for _, inserted := range []bool{true, false} {
		a, b := jackRouting(inserted), jackRouting(inserted)
		if len(a) != len(b) {
			t.Fatalf("inserted=%v: not deterministic", inserted)
		}
		for i := range a {
			if a[i].Ctl != b[i].Ctl || len(a[i].Args) != len(b[i].Args) {
				t.Errorf("inserted=%v: write %d differs between calls", inserted, i)
			}
		}
	}
}

func TestJackRoutingDriftRewritesOnlyWhatMoved(t *testing.T) {
	// Gain clobbered back to the floor, amp still correct — the exact state
	// measured after a mediaserver restart with a plug in.
	drift := jackRoutingDrift(nil, true, map[string]string{
		ctlSpeakerAmp:   "Off",
		ctlHPDriverGain: "0",
	})
	if len(drift) != 1 || drift[0].Ctl != ctlHPDriverGain {
		t.Fatalf("want only the gain rewritten, got %+v", drift)
	}
}

func TestJackRoutingDriftIsSilentWhenNothingMoved(t *testing.T) {
	if d := jackRoutingDrift(nil, true, map[string]string{
		ctlSpeakerAmp:   "Off",
		ctlHPDriverGain: hpGainJack,
	}); len(d) != 0 {
		t.Errorf("want no writes on a correct codec, got %+v", d)
	}
}

// A control we could not READ must not be treated as drifted. Rewriting on a
// failed read would spawn tinymix every interval forever on any device whose
// output we cannot parse — the same "failure to look is not evidence of
// absence" rule the controller's asset reconcile follows.
func TestJackRoutingDriftSkipsUnreadableControls(t *testing.T) {
	if d := jackRoutingDrift(nil, true, map[string]string{}); len(d) != 0 {
		t.Errorf("want no writes when nothing could be read, got %+v", d)
	}
	d := jackRoutingDrift(nil, true, map[string]string{ctlHPDriverGain: "0"})
	if len(d) != 1 || d[0].Ctl != ctlHPDriverGain {
		t.Errorf("want only the readable, drifted control, got %+v", d)
	}
}

// ── Echo Dot 3rd gen ─────────────────────────────────────────────────────────

func donutValue(ws []mixerWrite, ctl string) (string, int, bool) {
	for i, w := range ws {
		if w.Ctl == ctl {
			return w.Args[0], i, true
		}
	}
	return "", -1, false
}

// The values measured on 2026-09-29: stock's headphone path, which played
// through the jack at a usable level with EchoMuse stopped.
func TestDonutJackRoutingIsStocksHeadphonePath(t *testing.T) {
	in := jackRoutingFor(board.Donut, true)
	for ctl, want := range map[string]string{
		ctlDonutHPOut:        "AUDIO_AMP",
		ctlDonutLineOut:      "OPEN",
		ctlDonutHPAmp:        "3",
		ctlDonutHPGainL:      "-2dB",
		ctlDonutHPGainR:      "-2dB",
		mixer.PlaybackVolume: "0",
	} {
		if got, _, ok := donutValue(in, ctl); !ok || got != want {
			t.Errorf("plug in: %s = %q (set %v), want %q", ctl, got, ok, want)
		}
	}
}

// Removal must undo every output-selecting write, or the speaker stays muted
// after the cable comes out, which reads as a dead Echo.
func TestDonutJackRemovalRestoresTheSpeaker(t *testing.T) {
	out := jackRoutingFor(board.Donut, false)
	for ctl, want := range map[string]string{
		ctlDonutHPOut:        "OPEN",
		ctlDonutLineOut:      "VOICE_AMP",
		ctlDonutHPAmp:        "0",
		mixer.PlaybackVolume: unityVolume(board.Donut),
	} {
		if got, _, ok := donutValue(out, ctl); !ok || got != want {
			t.Errorf("plug out: %s = %q (set %v), want %q", ctl, got, ok, want)
		}
	}
	if unityVolume(board.Donut) != "255" {
		t.Error("the TAS2770's 0dB is 255; anything else leaves the speaker attenuated after removal")
	}
}

// Neither edge may play both outputs at once: the speaker is muted before the
// jack is connected, and the jack is disconnected before the speaker returns.
func TestDonutJackEdgesNeverPlayBoth(t *testing.T) {
	_, mute, _ := donutValue(jackRoutingFor(board.Donut, true), mixer.PlaybackVolume)
	_, hpOn, _ := donutValue(jackRoutingFor(board.Donut, true), ctlDonutHPOut)
	if mute > hpOn {
		t.Errorf("insert connects the jack (write %d) before muting the speaker (write %d)", hpOn, mute)
	}
	_, hpOff, _ := donutValue(jackRoutingFor(board.Donut, false), ctlDonutHPOut)
	_, unmute, _ := donutValue(jackRoutingFor(board.Donut, false), mixer.PlaybackVolume)
	if unmute < hpOff {
		t.Errorf("removal unmutes the speaker (write %d) before disconnecting the jack (write %d)", unmute, hpOff)
	}
}

// The amp gain is a stereo pair; one value would set only the left channel.
func TestDonutHeadphoneAmpWritesBothChannels(t *testing.T) {
	for _, inserted := range []bool{true, false} {
		for _, w := range jackRoutingFor(board.Donut, inserted) {
			if w.Ctl == ctlDonutHPAmp && (len(w.Args) != 2 || w.Args[0] != w.Args[1]) {
				t.Errorf("inserted=%v: %s needs two equal values, got %v", inserted, w.Ctl, w.Args)
			}
		}
	}
}

// Biscuit's controls do not exist on the Dot 3's card and the Dot 3's do not
// exist on biscuit's; a table that mixes them fails a write on every edge and
// every 30s reconcile.
func TestJackRoutingTablesStayOnTheirOwnBoard(t *testing.T) {
	donutOnly := map[string]bool{ctlDonutHPOut: true, ctlDonutLineOut: true, ctlDonutHPAmp: true, ctlDonutHPGainL: true, ctlDonutHPGainR: true}
	biscuitOnly := map[string]bool{ctlSpeakerAmp: true, ctlHPDriverGain: true}
	for _, inserted := range []bool{true, false} {
		for _, b := range []*board.Board{nil, board.Biscuit} {
			for _, w := range jackRoutingFor(b, inserted) {
				if donutOnly[w.Ctl] {
					t.Errorf("%s: biscuit table writes the Dot 3's %s", board.IDOf(b), w.Ctl)
				}
			}
		}
		for _, w := range jackRoutingFor(board.Donut, inserted) {
			if biscuitOnly[w.Ctl] {
				t.Errorf("Dot 3 table writes biscuit's %s", w.Ctl)
			}
		}
	}
}

// The reconciler covers the Dot 3 too: stock's daemon reacts to the jack, and
// if it ever rewrites a control the next pass puts it back.
func TestDonutJackDriftRestoresAMovedControl(t *testing.T) {
	d := jackRoutingDrift(board.Donut, true, map[string]string{
		ctlDonutHPOut:        "OPEN", // moved
		mixer.PlaybackVolume: "0",
		ctlDonutHPAmp:        "3",
	})
	if len(d) != 1 || d[0].Ctl != ctlDonutHPOut {
		t.Fatalf("want only HPOUT Mux rewritten, got %+v", d)
	}
}
