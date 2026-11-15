package tuya

import (
	"encoding/json"
	"errors"
)

const privacySwitchDp = "237"

var (
	errCameraOff     = errors.New("camera is off (privacy mode)")
	errCameraOffline = errors.New("camera is offline")
)

// cameraPowerError reads a tuya.m.device.get result. Values are compared as
// written, so an odd isOnline never skips the privacy check. Only false, "false"
// and 0 mean offline, a missing or other value means online. DP 237 is the
// privacy switch and true, "1" and 1 mean the camera is off.
func cameraPowerError(raw json.RawMessage) error {
	var device struct {
		IsOnline json.RawMessage            `json:"isOnline"`
		Dps      map[string]json.RawMessage `json:"dps"`
	}
	_ = json.Unmarshal(raw, &device)

	switch string(device.IsOnline) {
	case "false", `"false"`, "0":
		return errCameraOffline
	}

	switch string(device.Dps[privacySwitchDp]) {
	case "true", `"1"`, "1":
		return errCameraOff
	}

	return nil
}

// checkCameraPower fails without opening a session when the camera is off or
// offline. It asks the cloud at most once per backoff window, and a failed
// check is not held against the camera.
func checkCameraPower(client *TuyaOemApiClient) error {
	if err := oemOffBackoff.acquire(client.deviceId); err != nil {
		return err
	}

	raw, err := client.call("tuya.m.device.get", map[string]string{"devId": client.deviceId})
	if err != nil {
		oemOffBackoff.release(client.deviceId, nil)
		return nil
	}

	if err = cameraPowerError(raw); err != nil {
		oemOffBackoff.release(client.deviceId, err)
		return err
	}

	oemOffBackoff.reset(client.deviceId)
	return nil
}
