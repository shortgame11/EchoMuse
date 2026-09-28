// Package board identifies the hardware the firmware is running on and holds
// what differs per board as data (#541).
//
// Detection must be POSITIVE. An unrecognised device gets no board, and a nil
// board means "touch nothing": the kernel defaults stay in force, which for
// thermal policy are stricter than any profile here. Writing one board's
// thermal profile onto another is the failure this package exists to prevent.
//
// The device tree cannot identify a board on these kernels — every MT8163
// product reports model "MT8163" — so identity comes from Amazon's idme,
// which carries the product's device type id. A board whose device type id we
// have not read yet is identified instead by a part only it carries, found BY
// NAME (Probe) — the same rule as every other hardware lookup here.
package board

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Board is one supported piece of hardware.
type Board struct {
	// ID is the stable name reported to the controller.
	ID string
	// DeviceTypeID is Amazon's product id from /proc/idme/device_type_id.
	// Matched exactly.
	DeviceTypeID string
	// Tuning is the platform policy applied at boot on emOS. Nil means none.
	Tuning *Tuning
	// Probe identifies the board from its hardware when idme does not, and
	// must be POSITIVE: true only on a part this board alone carries. Nil
	// means idme only.
	Probe func(root string) bool
}

// Biscuit is the Echo Dot 2nd gen (MT8163). The same id was read off devices
// on the FireOS 5 and FireOS 6 kernels, under both FireOS and emOS.
var Biscuit = &Board{
	ID:           "biscuit",
	DeviceTypeID: "A3S5BH2HU6VAYF",
	Tuning:       biscuitTuning,
}

// Donut is the Echo Dot 3rd gen (donut_puffin, MT8167/MT8516), on stock
// FireOS 6. Its device type id has not been read off a unit yet, so it is
// identified by its speaker amp: a TI TAS2770 at i2c 2-0044, which biscuit
// does not have (its internal driver hangs off the tlv320aic32x4 codec).
// Everything that differs in the audio path keys off this board — see the
// Echo Dot 3rd gen section of device/CLAUDE.md. No thermal Tuning: nothing
// has been read off a stock Dot 3 to base one on, and nil touches nothing.
var Donut = &Board{
	ID:    "donut",
	Probe: func(root string) bool { return i2cName(root, "2-0044") == "tas2770" },
}

// Known is every board the firmware can identify.
var Known = []*Board{Biscuit, Donut}

// i2cName is an i2c client's driver name, or "" when there is none.
func i2cName(root, client string) string {
	b, err := os.ReadFile(filepath.Join(root, "/sys/bus/i2c/devices", client, "name"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

var (
	currentOnce sync.Once
	current     *Board
)

// Current is Detect("") for the device the firmware is running on, resolved
// once. Code that behaves differently per board asks this; nil (an unknown
// board) keeps the behaviour the firmware had before boards were told apart,
// which is biscuit's.
func Current() *Board {
	currentOnce.Do(func() { current = Detect("") })
	return current
}

// IsDonut reports whether the firmware is running on the Echo Dot 3rd gen.
func IsDonut() bool { return Current() == Donut }

// Detect returns the board beneath root, or nil when none matches. root is ""
// on a device and a fixture directory in tests.
func Detect(root string) *Board {
	if b := detectIdme(root); b != nil {
		return b
	}
	for _, k := range Known {
		if k.Probe != nil && k.Probe(root) {
			return k
		}
	}
	return nil
}

// detectIdme matches Amazon's device type id exactly.
func detectIdme(root string) *Board {
	b, err := os.ReadFile(filepath.Join(root, "/proc/idme/device_type_id"))
	if err != nil {
		return nil
	}
	// idme values are NUL-terminated (15 bytes for a 14-character id).
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return nil
	}
	for _, k := range Known {
		if k.DeviceTypeID != "" && k.DeviceTypeID == id {
			return k
		}
	}
	return nil
}

// IDOf is the board id for reporting, "unknown" when nil.
func IDOf(b *Board) string {
	if b == nil {
		return "unknown"
	}
	return b.ID
}
