package tuya

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Field names come from the Avent client: isOnline and dps with 237 as the
// privacy switch, where "1" means the camera is off.
const devicePrivacy = `{"success":true,"result":{"devId":"fake-device","isOnline":true,"dps":{"237":"1","138":false}}}`

func TestCameraPowerError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"on", `{"isOnline":true,"dps":{"237":"0","138":false}}`, ""},
		{"privacy on", `{"isOnline":true,"dps":{"237":"1","138":false}}`, "camera is off (privacy mode)"},
		{"offline wins over stale privacy", `{"isOnline":false,"dps":{"237":"1"}}`, "camera is offline"},
		{"missing fields count as on", `{"dps":{"138":false}}`, ""},
		{"odd isOnline still checks privacy", `{"isOnline":"yes","dps":{"237":"1"}}`, "camera is off (privacy mode)"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := cameraPowerError(json.RawMessage(test.body))
			if test.want == "" && err != nil {
				t.Errorf("error = %v, want none", err)
			}
			if test.want != "" && (err == nil || err.Error() != test.want) {
				t.Errorf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDialOffCameraOpensNoSession(t *testing.T) {
	var actions []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		actions = append(actions, r.PostForm.Get("a"))
		_, _ = w.Write([]byte(devicePrivacy))
	}))
	t.Cleanup(server.Close)

	// The dial builds its own client, which uses the default transport.
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })

	const deviceId = "fake-off-device"
	t.Cleanup(func() {
		oemOffBackoff.reset(deviceId)
		oemDialBackoff.reset(deviceId)
	})

	rawURL := "tuya://" + server.Listener.Addr().String() + "?device_id=" + deviceId + "&sid=fake-sid&ecode=fake-ecode" +
		"&partner_identity=fake-partner&app_key=fake-app-key&signing_key=fake-signing-key" +
		"&ch_key=fakech01&package_name=com.fake.app&client_device_id=fake-client-device"

	_, err := Dial(rawURL)
	if err == nil || err.Error() != "tuya: camera is off (privacy mode)" {
		t.Errorf("dial error = %v, want the off error", err)
	}

	// Pre-link, rtc config, user info and every MQTT step come after the power check.
	if !reflect.DeepEqual(actions, []string{"tuya.m.device.get"}) {
		t.Errorf("api actions = %v, want only tuya.m.device.get", actions)
	}

	// The off result is not a refusal.
	if err := oemDialBackoff.acquire(deviceId); err != nil {
		t.Errorf("refusal backoff after an off dial: %v", err)
	} else {
		oemDialBackoff.release(deviceId, nil)
	}
}
