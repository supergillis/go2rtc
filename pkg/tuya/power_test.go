package tuya

import (
	"bytes"
	"reflect"

	"encoding/json"
	"errors"
	"github.com/rs/zerolog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Field names come from the havent client: isOnline and dps with 237 as the
// privacy switch, where "1" means the camera is off.
const (
	deviceOn      = `{"success":true,"result":{"devId":"fake-device","isOnline":true,"dps":{"237":"0","138":false}}}`
	devicePrivacy = `{"success":true,"result":{"devId":"fake-device","isOnline":true,"dps":{"237":"1","138":false}}}`
	deviceOffline = `{"success":true,"result":{"devId":"fake-device","isOnline":false,"dps":{"237":"1"}}}`
	deviceFailed  = `{"success":false,"errorMsg":"SID_INVALID","errorCode":"E1"}`
	deviceNoField = `{"success":true,"result":{"devId":"fake-device","dps":{"138":false}}}`
)

func TestCameraPowerError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"on", deviceOn, ""},
		{"privacy on", devicePrivacy, "camera is off (privacy mode)"},
		{"offline wins over stale privacy", deviceOffline, "camera is offline"},
		{"missing fields count as on", deviceNoField, ""},
		{"string false is offline", `{"success":true,"result":{"isOnline":"false","dps":{"237":"0"}}}`, "camera is offline"},
		{"number zero is offline", `{"success":true,"result":{"isOnline":0,"dps":{"237":"0"}}}`, "camera is offline"},
		{"string true is online", `{"success":true,"result":{"isOnline":"true","dps":{"237":"0"}}}`, ""},
		{"odd isOnline still checks privacy", `{"success":true,"result":{"isOnline":"yes","dps":{"237":"1"}}}`, "camera is off (privacy mode)"},
		{"bool privacy value", `{"success":true,"result":{"isOnline":true,"dps":{"237":true}}}`, "camera is off (privacy mode)"},
		{"number privacy value", `{"success":true,"result":{"isOnline":true,"dps":{"237":1}}}`, "camera is off (privacy mode)"},
		{"bool false privacy value", `{"success":true,"result":{"isOnline":true,"dps":{"237":false}}}`, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response struct {
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal([]byte(test.body), &response); err != nil {
				t.Fatal(err)
			}

			err := cameraPowerError(response.Result)
			if test.want == "" && err != nil {
				t.Errorf("error = %v, want none", err)
			}
			if test.want != "" && (err == nil || err.Error() != test.want) {
				t.Errorf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCheckCameraPower(t *testing.T) {
	var body atomic.Value
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("a"); got != "tuya.m.device.get" {
			t.Errorf("action = %q, want tuya.m.device.get", got)
		}
		if got := r.PostForm.Get("postData"); got != `{"devId":"fake-device"}` {
			t.Errorf("postData = %q, want the device id", got)
		}
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(server.Close)

	client, err := NewTuyaOemApiClient("unused", "fake-device", fakeOemSession())
	if err != nil {
		t.Fatal(err)
	}
	client.apiUrl = server.URL

	var logs bytes.Buffer
	Logger = zerolog.New(&logs)
	t.Cleanup(func() { Logger = zerolog.Nop() })

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	backoff := newOffBackoff(func() time.Time { return now })

	type step struct {
		advance   time.Duration
		body      string
		wantErr   string
		wantCalls int32
	}

	steps := []step{
		{0, devicePrivacy, "camera is off (privacy mode)", 1},
		{30 * time.Second, devicePrivacy, "camera is off (privacy mode), next try in 30s", 1},
		{30 * time.Second, deviceOffline, "camera is offline", 2},
		{time.Minute, deviceOffline, "camera is offline, next try in 1m0s", 2},
		{time.Minute, deviceOffline, "camera is offline", 3},
		{4 * time.Minute, deviceOn, "", 4},
		{0, devicePrivacy, "camera is off (privacy mode)", 5},
		{59 * time.Second, devicePrivacy, "camera is off (privacy mode), next try in 1s", 5},
		{time.Second, deviceFailed, "", 6},
		{0, deviceOn, "", 7},
	}

	for index, step := range steps {
		now = now.Add(step.advance)
		body.Store(step.body)

		err := checkCameraPower(client, backoff, "fake-device")
		if step.wantErr == "" && err != nil {
			t.Fatalf("step %d: unexpected error %v", index, err)
		}
		if step.wantErr != "" && (err == nil || err.Error() != step.wantErr) {
			t.Fatalf("step %d: error = %v, want %q", index, err, step.wantErr)
		}
		if got := calls.Load(); got != step.wantCalls {
			t.Fatalf("step %d: device.get calls = %d, want %d", index, got, step.wantCalls)
		}
	}

	// The failed call leaves one debug line with the device id and no session id.
	want := `{"level":"debug","device_id":"fake-device","error":"tuya.m.device.get: SID_INVALID (code: E1)","message":"tuya: power check skipped"}` + "\n"
	if got := logs.String(); got != want {
		t.Errorf("log = %q, want %q", got, want)
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
		oemOffBackoff.succeed(deviceId)
		oemDialBackoff.succeed(deviceId)
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
	if session, err := oemDialBackoff.begin(deviceId); err != nil {
		t.Errorf("refusal backoff after an off dial: %v", err)
	} else {
		session.release()
	}
}

func TestOffResultKeepsTheRefusalSchedule(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	refusals := newDialBackoff(func() time.Time { return now }, nil)

	// An off camera ends the dial without a verdict.
	session, err := refusals.begin("fake-device")
	if err != nil {
		t.Fatal(err)
	}
	session.release()

	// A real refusal afterwards still starts at 30 s.
	session, err = refusals.begin("fake-device")
	if err != nil {
		t.Fatal(err)
	}
	session.failed(errors.New("refused"))

	_, err = refusals.begin("fake-device")
	want := "backing off after failed dial: refused, next try in 30s"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}
