package tuya

import (
	"encoding/json"
	"errors"
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
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(server.Close)

	client, err := NewTuyaOemApiClient("unused", "fake-device", fakeOemSession())
	if err != nil {
		t.Fatal(err)
	}
	client.apiUrl = server.URL

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
