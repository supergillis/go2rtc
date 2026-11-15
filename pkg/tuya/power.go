package tuya

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// Logger is set by the application, the default drops everything.
var Logger = zerolog.Nop()

const privacySwitchDp = "237"

var (
	errCameraOff     = errors.New("camera is off (privacy mode)")
	errCameraOffline = errors.New("camera is offline")
)

// oemOffBackoff spaces the device.get checks of a camera that is off.
var oemOffBackoff = newOffBackoff(time.Now)

// cameraPowerError reads a tuya.m.device.get result. The values are decoded
// loosely, so an odd isOnline never skips the privacy check. Only false, "false"
// and 0 mean offline, a missing or other value means online.
func cameraPowerError(raw json.RawMessage) error {
	var device struct {
		IsOnline any            `json:"isOnline"`
		Dps      map[string]any `json:"dps"`
	}
	_ = json.Unmarshal(raw, &device)

	switch online := device.IsOnline.(type) {
	case bool:
		if !online {
			return errCameraOffline
		}
	case string:
		if strings.EqualFold(online, "false") {
			return errCameraOffline
		}
	case float64:
		if online == 0 {
			return errCameraOffline
		}
	}

	// DP 237 is the privacy switch, "1" means the camera is off.
	switch privacy := device.Dps[privacySwitchDp].(type) {
	case bool:
		if privacy {
			return errCameraOff
		}
	default:
		if fmt.Sprint(privacy) == "1" {
			return errCameraOff
		}
	}
	return nil
}

// checkCameraPower fails without opening a session when the camera is off or
// offline. It asks the cloud at most once per backoff window, and a failed
// check itself is not held against the camera.
func checkCameraPower(client *TuyaOemApiClient, backoff *dialBackoff, deviceId string) error {
	session, err := backoff.begin(deviceId)
	if err != nil {
		return err
	}
	defer session.release()

	raw, err := client.call("tuya.m.device.get", map[string]string{"devId": deviceId})
	if err != nil {
		Logger.Debug().Str("device_id", deviceId).Err(err).Msg("tuya: power check skipped")
		return nil
	}

	if err = cameraPowerError(raw); err != nil {
		session.failed(err)
		return err
	}

	session.succeeded()
	return nil
}
