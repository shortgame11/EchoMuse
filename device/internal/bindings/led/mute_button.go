package led

import (
	"fmt"
	"os"
	"strings"
)

// Mute-button LED — the discrete red LED under the mic-off button, separate
// from the 12-LED ring. The line is SoC GPIO bank 5 bit 7 = pin 87 = sysfs
// gpio444, ACTIVE-HIGH (1 = lit). Found by regmap-tracing stock FireOS on a
// live biscuit (2026-07-19): each mute press writes pinctrl DIR-set 0x54
// then DOUT-set 0x454 (on) / DOUT-clr 0x458 (off), bit 7, bank 5.
//
// Do not trust libled_hal.so here: its k_muteButtonGPIOAddress = 0x1BD (445)
// is off by one from the kernel's sysfs numbering — pin 88's pad is muxed to
// MSDC2_DAT1 and writes to gpio445 reach nothing (v2.9.4 and earlier drove
// it; the button never lit). Stock itself bypasses sysfs via the /dev/mtgpio
// ioctl, which is why the HAL constant never had to agree with gpiolib.
//
// NOT ON THE ECHO DOT 3RD GEN. There the gpiochip base is 387, so gpio444 is
// SoC pin 57, and on that board pin 57 sits in its mode-4 audio function.
// Exporting it through sysfs switches it to plain GPIO (and this code then
// drives it low), which breaks the speaker's serial clocks: the TAS2770 amp
// latches a TDM clock error and stays in shutdown until reboot. Measured
// 2026-09-28: clean boot "57: 4 0 0 1 ...", after EchoMuse "57: 0 1 0 0 ...",
// and silence from every playback path until the pin was restored. The Dot
// 3's mute LED is not yet identified, so on that board it is not driven.
const (
	muteButtonGPIO      = "444"
	gpioExportPath      = "/sys/class/gpio/export"
	muteButtonDirPath   = "/sys/class/gpio/gpio" + muteButtonGPIO + "/direction"
	muteButtonValuePath = "/sys/class/gpio/gpio" + muteButtonGPIO + "/value"
)

// muteLEDUnsafe reports whether gpio444 is NOT the mute LED on this board —
// true on the Echo Dot 3rd gen, identified by its TAS2770 speaker amp.
func muteLEDUnsafe() bool {
	b, _ := os.ReadFile("/sys/bus/i2c/devices/2-0044/name")
	return strings.TrimSpace(string(b)) == "tas2770"
}

// InitMuteButtonLED exports the GPIO if needed, forces output direction,
// and switches the LED off (the process starts unmuted; a crash while
// muted must not leave a stale red button on restart).
func InitMuteButtonLED() error {
	if muteLEDUnsafe() {
		return nil
	}
	if _, err := os.Stat(muteButtonValuePath); os.IsNotExist(err) {
		if err := os.WriteFile(gpioExportPath, []byte(muteButtonGPIO), 0644); err != nil {
			return fmt.Errorf("mute button LED: export gpio%s: %w", muteButtonGPIO, err)
		}
	}
	if err := os.WriteFile(muteButtonDirPath, []byte("out"), 0644); err != nil {
		return fmt.Errorf("mute button LED: set direction: %w", err)
	}
	return SetMuteButtonLED(false)
}

// SetMuteButtonLED switches the red LED under the mic-off button.
// Active-high (see package comment): 1 = on, 0 = off.
func SetMuteButtonLED(on bool) error {
	if muteLEDUnsafe() {
		return nil
	}
	v := []byte("0")
	if on {
		v = []byte("1")
	}
	if err := os.WriteFile(muteButtonValuePath, v, 0644); err != nil {
		return fmt.Errorf("mute button LED: write value: %w", err)
	}
	return nil
}