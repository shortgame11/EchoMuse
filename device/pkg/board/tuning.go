package board

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Tuning is kernel policy a vendor userspace would normally set at boot:
// hotplug thresholds and the thermal profile. It is procfs/sysfs, so it is
// lost on reboot and applied on every boot.
type Tuning struct {
	// Values are files written verbatim and read back equal.
	Values []Value
	// Proc are MTK procfs command writes. Their read-back format differs from
	// what is written, so each carries the text expected afterwards.
	Proc []ProcWrite
	// ZoneTrips sets trip temperatures on a thermal zone found by type.
	ZoneTrips []ZoneTrips
	// CoolerLevels sets per-state values on a cooling device found by type.
	CoolerLevels []CoolerLevels
	// Coolers must all exist by type before anything is written — a profile
	// binding trips to a cooler this kernel lacks is the wrong profile.
	Coolers []string
}

type Value struct{ Path, Value string }

type ProcWrite struct {
	Path  string
	Lines []string // one write per line, in order
	// Expect is matched against the file with whitespace collapsed.
	Expect []string
}

type ZoneTrips struct {
	Zone  string      // thermal zone type
	Trips map[int]int // trip index → millidegrees
}

type CoolerLevels struct {
	Cooler string
	// Levels maps the index written to its value. The kernel takes these
	// 0-based but lists them 1-based: writing "6 251" changes the line read
	// back as "7 251".
	Levels map[int]int
}

const (
	thermalDir = "/sys/class/thermal"
	// stock's thermal_manager writes this; see the biscuit profile
	tzcpuPath = "/proc/driver/thermal/tzcpu"
)

// donutTuning is the thermal policy for the Echo Dot 3 (MT8167B).
// CoolerLevels and Proc writes should be populated by reading off a stock FireOS 6 device.
var donutTuning = &Tuning{
    ZoneTrips: []ZoneTrips{
        {
            Zone:  "skin_virtual",
            Trips: map[int]int{0: 56000, 1: 57000, 2: 58000, 3: 59000, 4: 60000, 5: 61000, 6: 71000},
        },
        {
            Zone:  "case_virtual",
            Trips: map[int]int{0: 55000, 1: 56000, 2: 57000, 3: 58000, 4: 59000, 5: 60000, 6: 70000},
        },
        {
            Zone:  "mtktscpu",
            Trips: map[int]int{0: 90000, 1: 93000, 2: 96000, 3: 99000, 4: 102000, 5: 105000, 6: 107000, 7: 110000, 8: 115000},
        },
    },
    Coolers: []string{
        "mtktscpu-sysrst", "cpu_adaptive_0", "cpu_adaptive_1", "cpu_adaptive_2",
        "thermal-cpufreq-0", "audio_cooler", "led_cooler", "suspend_cooler", "wifi",
    },
    // Populate Proc and CoolerLevels here once dumped from a stock device
}

// Apply writes t beneath root and verifies it. Everything the profile touches
// is resolved first; if anything is missing nothing is written. The returned
// lines are for the log whatever the outcome.
func Apply(root string, t *Tuning) ([]string, error) {
	var log []string
	if t == nil {
		return log, nil
	}

	zones, err := byType(root, "thermal_zone")
	if err != nil {
		return log, err
	}
	coolers, err := byType(root, "cooling_device")
	if err != nil {
		return log, err
	}
	for _, c := range t.Coolers {
		if _, ok := coolers[c]; !ok {
			return log, fmt.Errorf("cooler %q not present — profile does not fit, nothing written", c)
		}
	}
	for _, z := range t.ZoneTrips {
		if _, ok := zones[z.Zone]; !ok {
			return log, fmt.Errorf("thermal zone %q not present — nothing written", z.Zone)
		}
	}
	for _, c := range t.CoolerLevels {
		if _, ok := coolers[c.Cooler]; !ok {
			return log, fmt.Errorf("cooler %q not present — nothing written", c.Cooler)
		}
	}
	for _, v := range t.Values {
		if _, err := os.Stat(filepath.Join(root, v.Path)); err != nil {
			return log, fmt.Errorf("%s not present — nothing written", v.Path)
		}
	}
	for _, p := range t.Proc {
		if _, err := os.Stat(filepath.Join(root, p.Path)); err != nil {
			return log, fmt.Errorf("%s not present — nothing written", p.Path)
		}
	}

	var failed []string
	fail := func(f string, a ...interface{}) { failed = append(failed, fmt.Sprintf(f, a...)) }

	for _, v := range t.Values {
		p := filepath.Join(root, v.Path)
		if err := write(p, v.Value); err != nil {
			fail("%s: %v", v.Path, err)
			continue
		}
		if got := read(p); got != v.Value {
			fail("%s reads %q, want %q", v.Path, got, v.Value)
		}
	}

	for _, pw := range t.Proc {
		p := filepath.Join(root, pw.Path)
		for _, l := range pw.Lines {
			if err := write(p, l); err != nil {
				fail("%s: %v", pw.Path, err)
			}
		}
		got := collapse(read(p))
		for _, e := range pw.Expect {
			if !strings.Contains(got, e) {
				fail("%s does not read back %q", pw.Path, e)
			}
		}
	}

	for _, z := range t.ZoneTrips {
		dir := zones[z.Zone]
		// Highest index first, so the trips never pass through an inverted order.
		idx := make([]int, 0, len(z.Trips))
		for i := range z.Trips {
			idx = append(idx, i)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(idx)))
		for _, i := range idx {
			p := filepath.Join(dir, fmt.Sprintf("trip_point_%d_temp", i))
			want := strconv.Itoa(z.Trips[i])
			if err := write(p, want); err != nil {
				fail("%s trip %d: %v", z.Zone, i, err)
				continue
			}
			if got := read(p); got != want {
				fail("%s trip %d reads %q, want %s", z.Zone, i, got, want)
			}
		}
	}

	for _, c := range t.CoolerLevels {
		p := filepath.Join(coolers[c.Cooler], "levels")
		for i, v := range c.Levels {
			if err := write(p, fmt.Sprintf("%d %d", i, v)); err != nil {
				fail("%s level %d: %v", c.Cooler, i, err)
			}
		}
		got := read(p)
		for i, v := range c.Levels {
			if !hasLine(got, fmt.Sprintf("%d %d", i+1, v)) {
				fail("%s level %d does not read back %d", c.Cooler, i, v)
			}
		}
	}

	if len(failed) > 0 {
		return failed, fmt.Errorf("%d setting(s) did not take", len(failed))
	}
	log = append(log, fmt.Sprintf("%d values, %d proc writes, %d zones, %d coolers applied and verified",
		len(t.Values), len(t.Proc), len(t.ZoneTrips), len(t.CoolerLevels)))
	return log, nil
}

// byType maps each thermal zone or cooling device's type to its directory. A
// type listed twice is ambiguous, so it is dropped rather than guessed.
func byType(root, prefix string) (map[string]string, error) {
	dirs, err := filepath.Glob(filepath.Join(root, thermalDir, prefix+"*"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	dup := map[string]bool{}
	for _, d := range dirs {
		t := read(filepath.Join(d, "type"))
		if t == "" {
			continue
		}
		if _, seen := out[t]; seen {
			dup[t] = true
		}
		out[t] = d
	}
	for t := range dup {
		delete(out, t)
	}
	return out, nil
}

func write(path, val string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(val + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func hasLine(s, want string) bool {
	for _, l := range strings.Split(s, "\n") {
		if collapse(l) == want {
			return true
		}
	}
	return false
}
