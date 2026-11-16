package tuya

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestOemDialBackoff(t *testing.T) {
	type step struct {
		advance time.Duration
		action  string // acquire, failure, stop, stable
		camera  string
		wantErr string
	}

	refused := func(remaining string) string {
		return "mqtt: disconnect, next try in " + remaining
	}
	const inProgress = "dial in progress"

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "dial within the cooldown fails fast and names the last error",
			steps: []step{
				{0, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{10 * time.Second, "acquire", "cam", refused("20s")},
				{20 * time.Second, "acquire", "cam", ""},
			},
		},
		{
			name: "cooldown doubles per failure and caps at ten minutes",
			steps: []step{
				{0, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{30 * time.Second, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{59 * time.Second, "acquire", "cam", refused("1s")},
				{time.Second, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{119 * time.Second, "acquire", "cam", refused("1s")},
				{time.Second, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{4 * time.Minute, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{8 * time.Minute, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{0, "acquire", "cam", refused("10m0s")},
				{10 * time.Minute, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{0, "acquire", "cam", refused("10m0s")},
			},
		},
		{
			name: "a second dial while one is in flight fails fast",
			steps: []step{
				{0, "acquire", "cam", ""},
				{0, "acquire", "cam", inProgress},
				{0, "acquire", "other", ""},
				{0, "stop", "cam", ""},
				{0, "acquire", "cam", ""},
			},
		},
		{
			name: "a stable session resets the cooldown",
			steps: []step{
				{0, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{30 * time.Second, "acquire", "cam", ""},
				{stableSession, "stable", "cam", ""},
				{0, "acquire", "cam", ""},
				{0, "failure", "cam", ""},
				{0, "acquire", "cam", refused("30s")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				original := oemDialBackoff.cameras
				oemDialBackoff.cameras = nil
				t.Cleanup(func() { oemDialBackoff.cameras = original })

				clients := make(map[string]*Client)

				for index, step := range test.steps {
					time.Sleep(step.advance)
					client := clients[step.camera]

					switch step.action {
					case "acquire":
						err := oemDialBackoff.acquire(step.camera)
						if step.wantErr == "" && err != nil {
							t.Fatalf("step %d: unexpected error %v", index, err)
						}
						if step.wantErr != "" && (err == nil || err.Error() != step.wantErr) {
							t.Fatalf("step %d: error = %v, want %q", index, err, step.wantErr)
						}
						if err == nil {
							clients[step.camera] = newJudgedClient(step.camera)
						}
					case "failure":
						client.judgeFailure(errors.New("mqtt: disconnect"))
					case "stop":
						client.judgeStop()
					case "stable":
						client.judgeStable()
					}
				}
			})
		})
	}
}

// newJudgedClient is a dial that already sent its offer, so errors count.
func newJudgedClient(camera string) *Client {
	client := &Client{oem: true, deviceId: camera}
	client.asked.Store(true)
	return client
}
