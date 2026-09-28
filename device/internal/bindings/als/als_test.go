package als

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The threshold policy decides what reaches Home Assistant promptly and what
// waits up to 30s for the stats tick. Too sensitive and a flickering lamp
// floods the control plane; too coarse and "someone turned a light on" is the
// thing it misses.
func TestSignificant(t *testing.T) {
	cases := []struct {
		name          string
		baseline, now int
		want          bool
	}{
		// Measured noise on a still room: 309/311/313/308/312 on consecutive
		// reads, about ±1.5%. None of it may report.
		{"still room drift up", 309, 313, false},
		{"still room drift down", 313, 308, false},

		// The case this exists for.
		{"lamp switched on", 40, 300, true},
		{"lamp switched off", 300, 40, true},

		// Hand over the sensor, measured 309 -> 0.
		{"covered", 309, 0, true},
		{"uncovered", 0, 308, true},

		// Near darkness must not produce infinite ratios: a 2 lux wobble in a
		// dark room is not a room lighting up.
		{"tiny change near zero", 0, 2, false},
		{"tiny change near zero, down", 3, 0, false},

		// Big RELATIVE change but small absolute — still noise-ish.
		{"5 to 9 lux", 5, 9, false},

		// Daylight: 50 lux is invisible against 20000 and must not report,
		// which an absolute threshold would get wrong.
		{"daylight jitter", 20000, 20050, false},
		{"cloud clears", 8000, 20000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Significant(c.baseline, c.now); got != c.want {
				t.Fatalf("Significant(%d, %d) = %v, want %v",
					c.baseline, c.now, got, c.want)
			}
		})
	}
}

// Symmetry matters: a light going off should be as reportable as one coming
// on. Comparing against the baseline rather than the new value is what makes
// that true, and it is easy to get backwards.
func TestSignificantIsSymmetricEnough(t *testing.T) {
	if !Significant(300, 40) || !Significant(40, 300) {
		t.Fatal("a lamp must be reportable in both directions")
	}
}

// ── Status reporting (issue #90) ─────────────────────────────────────────────
//
// Two users' Dots reported no ambient light sensor and there was no way to
// tell whether the chip was absent or the driver had not bound, because the
// answer was only ever written to a log file on the device. These pin the
// three verdicts against a fake i2c bus.

// fakeBus builds an i2c tree and points the package at it. Returns the root.
// Also resets the package's cached state, which otherwise leaks between tests
// and makes the second one assert against the first one's scan.
func fakeBus(t *testing.T, devices map[string]bool) {
	t.Helper()
	root := t.TempDir()
	for name, withAttr := range devices {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "name"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if withAttr {
			if err := os.WriteFile(filepath.Join(dir, "als_lux"), []byte("42\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	mu.Lock()
	path, lastScan, reported = "", time.Time{}, false
	status = Status{Code: StatusUnknown}
	mu.Unlock()

	old, oldIIO := i2cGlob, iioGlob
	i2cGlob = filepath.Join(root, "*", "name")
	// No IIO devices unless a test adds them — never the build machine's own.
	iioGlob = filepath.Join(t.TempDir(), "*", "name")
	t.Cleanup(func() {
		i2cGlob, iioGlob = old, oldIIO
		mu.Lock()
		path, lastScan, reported = "", time.Time{}, false
		status = Status{Code: StatusUnknown}
		mu.Unlock()
	})
}

func TestStatusOK(t *testing.T) {
	fakeBus(t, map[string]bool{"tsl2540": true, "tsl2584tsv": false})
	got := Report()
	if got.Code != StatusOK {
		t.Fatalf("code = %q, want %q (detail %q)", got.Code, StatusOK, got.Detail)
	}
	if got.Path == "" {
		t.Fatal("a resolved sensor must report where it was found")
	}
	if !Present() {
		t.Fatal("Present() must be true when the sensor resolves")
	}
}

// Seen must list the WHOLE bus even when the sensor is found early.
//
// The first version returned as soon as it matched, so a working device
// reported only the names sorting before tsl2540 — on real hardware that
// dropped is31fl3236, tlv320aic32x4 and bq24297. Comparing a healthy bus
// against a broken one is what this field is for, so a truncated list from
// the healthy side defeats the purpose. The fixtures missed it because none
// had a device sorting after the match; `zz_after` is here to guarantee one
// always does.
func TestSeenListsWholeBusWhenSensorFound(t *testing.T) {
	fakeBus(t, map[string]bool{
		"aa_before": false,
		"tsl2540":   true,
		"zz_after":  false,
	})
	got := Report()
	if got.Code != StatusOK {
		t.Fatalf("code = %q, want %q", got.Code, StatusOK)
	}
	if len(got.Seen) != 3 {
		t.Fatalf("Seen = %v, want all three bus devices", got.Seen)
	}
	var sawAfter bool
	for _, s := range got.Seen {
		if s == "zz_after" {
			sawAfter = true
		}
	}
	if !sawAfter {
		t.Fatalf("Seen = %v, missing the device that sorts after the sensor", got.Seen)
	}
}

// The hypothesis for #90: these units carry the second ALS, which is present
// on working devices too but has no usable driver, and not the tsl2540.
func TestStatusNoChip(t *testing.T) {
	fakeBus(t, map[string]bool{"tsl2584tsv": false, "tlv320aic3101": false})
	got := Report()
	if got.Code != StatusNoChip {
		t.Fatalf("code = %q, want %q", got.Code, StatusNoChip)
	}
	// Seen is what makes the answer verifiable rather than merely asserted,
	// and identifies an unfamiliar revision the first time one appears.
	if len(got.Seen) != 2 {
		t.Fatalf("Seen = %v, want both bus devices", got.Seen)
	}
	if Present() {
		t.Fatal("Present() must be false with no tsl2540")
	}
}

// Distinct from no_chip because the fixes are opposite: this one is ours.
func TestStatusNoAttribute(t *testing.T) {
	fakeBus(t, map[string]bool{"tsl2540": false})
	got := Report()
	if got.Code != StatusNoAttribute {
		t.Fatalf("code = %q, want %q", got.Code, StatusNoAttribute)
	}
	if got.Detail == "" {
		t.Fatal("a failure must carry a detail a human can read in a bundle")
	}
}

// The absence LOG is deliberately once-only; the status must not be, or a
// device whose bus changed would keep reporting its first answer forever.
func TestStatusRefreshesAcrossScans(t *testing.T) {
	fakeBus(t, map[string]bool{"tsl2584tsv": false})
	if got := Report(); got.Code != StatusNoChip {
		t.Fatalf("first scan: code = %q, want %q", got.Code, StatusNoChip)
	}
	mu.Lock()
	lastScan = time.Time{} // allow an immediate re-scan
	mu.Unlock()
	if got := Report(); got.Code != StatusNoChip {
		t.Fatalf("second scan: code = %q, want %q", got.Code, StatusNoChip)
	}
}

// fakeIIO adds IIO devices (name -> attribute file -> contents) and points the
// package at them. Call after fakeBus, which resets the scan state.
func fakeIIO(t *testing.T, devices map[string]map[string]string) {
	t.Helper()
	root := t.TempDir()
	i := 0
	for name, attrs := range devices {
		dir := filepath.Join(root, "iio:device"+string(rune('0'+i)))
		i++
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "name"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for a, v := range attrs {
			if err := os.WriteFile(filepath.Join(dir, a), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	iioGlob = filepath.Join(root, "*", "name")
}

// The Echo Dot 3rd gen, as measured 2026-09-28: tsl2572 and opt3001 both on
// the i2c bus, neither with als_lux, and the tsl2572 bound as IIO device 0.
func TestDot3SensorIsFoundThroughIIO(t *testing.T) {
	fakeBus(t, map[string]bool{"tsl2572": false, "opt3001": false, "tas2770": false})
	fakeIIO(t, map[string]map[string]string{
		"tsl2572": {"in_illuminance0_input": "56.973000\n", "in_intensity0_raw": "812\n"},
	})
	got := Report()
	if got.Code != StatusOK {
		t.Fatalf("code = %q, want %q (%s)", got.Code, StatusOK, got.Detail)
	}
	if filepath.Base(got.Path) != "in_illuminance0_input" {
		t.Fatalf("path = %q, want the IIO lux attribute", got.Path)
	}
	lux := Lux()
	if lux == nil || *lux != 57 {
		t.Fatalf("Lux() = %v, want 57 (56.973 rounded)", lux)
	}
}

// Covered, the Dot 3 reads 0.000000 — a real reading, which must be 0 and not
// nil (nil means no sensor).
func TestIIOZeroIsAReading(t *testing.T) {
	fakeBus(t, map[string]bool{"tsl2572": false})
	fakeIIO(t, map[string]map[string]string{"tsl2572": {"in_illuminance0_input": "0.000000\n"}})
	lux := Lux()
	if lux == nil || *lux != 0 {
		t.Fatalf("Lux() = %v, want 0", lux)
	}
}

// Only named sensors count: an IIO device of another kind that happens to
// have an illuminance attribute is not assumed to be ours.
func TestUnknownIIODeviceIsNotUsed(t *testing.T) {
	fakeBus(t, map[string]bool{"opt3001": false})
	fakeIIO(t, map[string]map[string]string{"somethingelse": {"in_illuminance0_input": "12\n"}})
	got := Report()
	if got.Code != StatusNoChip {
		t.Fatalf("code = %q, want %q", got.Code, StatusNoChip)
	}
	var sawIIO bool
	for _, s := range got.Seen {
		if s == "iio:somethingelse" {
			sawIIO = true
		}
	}
	if !sawIIO {
		t.Fatalf("Seen = %v, want the IIO device listed", got.Seen)
	}
}

// biscuit keeps its own sensor even if an IIO one were present.
func TestBiscuitSensorComesFirst(t *testing.T) {
	fakeBus(t, map[string]bool{"tsl2540": true})
	fakeIIO(t, map[string]map[string]string{"tsl2572": {"in_illuminance0_input": "99.5\n"}})
	got := Report()
	if filepath.Base(got.Path) != "als_lux" {
		t.Fatalf("path = %q, want biscuit's als_lux", got.Path)
	}
	if lux := Lux(); lux == nil || *lux != 42 {
		t.Fatalf("Lux() = %v, want 42 from als_lux", lux)
	}
}

func TestParseLux(t *testing.T) {
	for in, want := range map[string]int{
		"309\n": 309, "0": 0, "56.973000\n": 57, "0.000000": 0, "0.4": 0, "0.5": 1, " 12 ": 12,
	} {
		if got, ok := parseLux(in); !ok || got != want {
			t.Errorf("parseLux(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1", "NaN", "+Inf"} {
		if _, ok := parseLux(bad); ok {
			t.Errorf("parseLux(%q) accepted", bad)
		}
	}
}
