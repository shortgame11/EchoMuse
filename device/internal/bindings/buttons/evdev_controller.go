package buttons

import (
	"context"
	"errors"
	"os/exec"
	"time"

	evdev "github.com/gvalkov/golang-evdev"
	"github.com/wilbowes/EchoMuse/pkg/board"
	"github.com/wilbowes/EchoMuse/pkg/buttons"
)

// The input devices the keys arrive on, which differ per board:
//
//	biscuit:  event1 = action (dot) + mute,  event2 = volume up/down
//	Dot 3:    event3 = action + volume,      event1 = mute (privacy)
//
// What a key MEANS comes from its key code (buttonTypeOf), never from which
// device carried it, so one loop serves both layouts. Anything not positively
// the Dot 3 keeps biscuit's devices, as every build did before.
const dotButton = "/dev/input/event1"
const volumeButton = "/dev/input/event2"

const donutKeysButton = "/dev/input/event3"
const donutPrivacyButton = "/dev/input/event1"

func buttonDevices(b *board.Board) []string {
	if b == board.Donut {
		return []string{donutKeysButton, donutPrivacyButton}
	}
	return []string{dotButton, volumeButton}
}

// buttonTypeOf is the button a key code belongs to: the volume keys are the
// volume button, everything else (action, mute) the dot button — biscuit's
// device split, expressed by code.
func buttonTypeOf(c buttons.ClickType) buttons.ButtonType {
	if c == buttons.VolumeUpClick || c == buttons.VolumeDownClick {
		return buttons.VolumeButton
	}
	return buttons.DotButton
}

// VolumeCallback is called on volume button release with direction "up" or "down".
type VolumeCallback func(direction string)

// MuteCallback is called on mute button release.
type MuteCallback func()

type EvDevController struct {
	volumeCallback func(direction string)
	muteCallback   func()
}

// SetVolumeCallback registers a function to be called on volume button events.
// Must be called before SubscribeToButton.
func (e *EvDevController) SetVolumeCallback(cb func(direction string)) {
	e.volumeCallback = cb
}

// SetMuteCallback registers a function to be called on mute button events.
// Must be called before SubscribeToButton.
func (e *EvDevController) SetMuteCallback(cb func()) {
	e.muteCallback = cb
}

// Init the button listeners
// Kills alexa's native button functions
func (e *EvDevController) Init() error {
	svc := "acebutton"
	if board.IsDonut() {
		svc = "acebuttond" // the Dot 3's name for Amazon's button daemon
	}
	cmd := exec.Command("stop", svc)
	return cmd.Run()
}

func (e *EvDevController) SubscribeToButton(callback buttons.ButtonClickCallback) (*buttons.EventSubscription, error) {
	if callback == nil {
		return nil, errors.New("callback can't be nil")
	}

	var devices []*evdev.InputDevice
	for _, path := range buttonDevices(board.Current()) {
		d, err := evdev.Open(path)
		if err != nil {
			for _, open := range devices {
				open.Release()
			}
			return nil, err
		}
		devices = append(devices, d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	eventSub := buttons.NewEventSubscription(cancel)

	readBtn := func(btnDevice *evdev.InputDevice) {
		defer btnDevice.Release()

		beforeClickType := buttons.ClickType(0)
		beforeDown := false
		// When each click type was pressed, so a release can report how long
		// it was held. Keyed by click type because the dot device carries the
		// mute button too, and interleaving the two must not attribute one
		// button's hold to the other.
		downAt := map[buttons.ClickType]time.Time{}

		for {
			if ctx.Err() != nil {
				return
			}

			inputEvent, err := btnDevice.ReadOne()
			if err != nil {
				return
			}

			// Only key events. Every key press is followed immediately by
			// an EV_SYN separator whose Code and Value are both 0 — and
			// without this filter that SYN fell through to the Code==0
			// branch, took the previous click type, computed Value==1 as
			// FALSE, and fired a "release" microseconds after the press.
			//
			// So the button has always acted on the SYN rather than on the
			// real release, which is why it felt instant and why the actual
			// release (a genuine transition to 0) was then swallowed as a
			// no-change. Invisible until something needed to know how long
			// the button was held: heldMs came out at ~0 every time.
			if inputEvent.Type != evdev.EV_KEY {
				continue
			}

			clickType := buttons.ClickType(inputEvent.Code)
			if inputEvent.Code != 0 {
				beforeClickType = clickType
			} else {
				clickType = beforeClickType
			}

			down := inputEvent.Value == 1
			if beforeDown == down {
				continue
			}
			beforeDown = down

			btn := buttons.Button{Type: buttonTypeOf(clickType)}

			// Intercept volume events
			if btn.Type == buttons.VolumeButton && !down {
				switch clickType {
				case buttons.VolumeUpClick:
					if e.volumeCallback != nil {
						e.volumeCallback("up")
					}
				case buttons.VolumeDownClick:
					if e.volumeCallback != nil {
						e.volumeCallback("down")
					}
				}
				continue
			}

			// Intercept mute
			if btn.Type == buttons.DotButton && !down && clickType == buttons.MuteClick {
				if e.muteCallback != nil {
					e.muteCallback()
				}
				continue
			}

			var heldMs int64
			if down {
				downAt[clickType] = time.Now()
			} else if t, ok := downAt[clickType]; ok {
				heldMs = time.Since(t).Milliseconds()
				delete(downAt, clickType)
			}

			callback(buttons.ButtonClickEvent{
				Button:    btn,
				ClickType: clickType,
				Down:      down,
				HeldMs:    heldMs,
			})
		}
	}

	for _, d := range devices {
		go readBtn(d)
	}

	return eventSub, nil
}

func (e *EvDevController) GetVolumeButton() buttons.Button {
	return buttons.Button{
		Type: buttons.VolumeButton,
	}
}

func (e *EvDevController) GetDotButton() buttons.Button {
	return buttons.Button{
		Type: buttons.DotButton,
	}
}

func NewButtonController() (*EvDevController, error) {
	controller := &EvDevController{}
	if err := controller.Init(); err != nil {
		return nil, err
	}
	return controller, nil
}