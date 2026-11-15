package tuya

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const privacySwitchDp = "237"

var (
	errCameraOff     = errors.New("camera is off (privacy mode)")
	errCameraOffline = errors.New("camera is offline")
)

// oemOffBackoff spaces the device.get checks of a camera that is off.
var oemOffBackoff = newOffBackoff(time.Now)

// cameraPowerError reads a tuya.m.device.get result. A missing field counts as
// on, as the field names are only known from the havent client.
func cameraPowerError(raw json.RawMessage) error {
	var device struct {
		IsOnline *bool          `json:"isOnline"`
		Dps      map[string]any `json:"dps"`
	}
	if err := json.Unmarshal(raw, &device); err != nil {
		return nil
	}

	if device.IsOnline != nil && !*device.IsOnline {
		return errCameraOffline
	}

	// DP 237 is the privacy switch, "1" means the camera is off.
	if fmt.Sprint(device.Dps[privacySwitchDp]) == "1" {
		return errCameraOff
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
		return nil
	}

	if err = cameraPowerError(raw); err != nil {
		session.failed(err)
		return err
	}

	session.succeeded()
	return nil
}
