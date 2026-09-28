package buttons

import (
	"context"
	"errors"
	"os/exec"
	"time"

	evdev "github.com/gvalkov/golang-evdev"
	"github.com/wilbowes/EchoMuse/pkg/buttons"
)

// Echo Dot 3 groups Action and Volume on event3, and Mute on event1
const keysDevicePath = "/dev/input/event3"
const privacyDevicePath = "/dev/input/event1"

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
	// Echo Dot 3 uses acebuttond
	cmd := exec.Command("stop", "acebuttond")
	return cmd.Run()
}

func (e *EvDevController) SubscribeToButton(callback buttons.ButtonClickCallback) (*buttons.EventSubscription, error) {
	if callback == nil {
		return nil, errors.New("callback can't be nil")
	}

	keysDevice, err := evdev.Open(keysDevicePath)
	if err != nil {
		return nil, err
	}
	privacyDevice, err := evdev.Open(privacyDevicePath)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	eventSub := buttons.NewEventSubscription(cancel)

	readBtn := func(btn buttons.Button, btnDevice *evdev.InputDevice) {
		defer btnDevice.Release()

		beforeClickType := buttons.ClickType(0)
		beforeDown := false
		downAt := map[buttons.ClickType]time.Time{}

		for {
			if ctx.Err() != nil {
				return
			}

			inputEvent, err := btnDevice.ReadOne()
			if err != nil {
				return
			}

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

			// Intercept volume and mute events based directly on clickType
			if !down {
				switch clickType {
				case buttons.VolumeUpClick:
					if e.volumeCallback != nil {
						e.volumeCallback("up")
					}
					continue
				case buttons.VolumeDownClick:
					if e.volumeCallback != nil {
						e.volumeCallback("down")
					}
					continue
				case buttons.MuteClick:
					if e.muteCallback != nil {
						e.muteCallback()
					}
					continue
				}
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

	go readBtn(e.GetDotButton(), keysDevice)
	// Since Mute and Volume interception now rely on clickType, the Button metadata 
	// passed here only affects un-intercepted events (which event1 has none of).
	go readBtn(e.GetVolumeButton(), privacyDevice)

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