package tuya

import (
	"errors"
	"testing"
	"time"
)

type backoffHarness struct {
	now      time.Time
	backoff  *dialBackoff
	sessions map[string]*dialSession
	timers   map[string]func()
}

func newBackoffHarness() *backoffHarness {
	harness := &backoffHarness{
		now:      time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
		sessions: make(map[string]*dialSession),
		timers:   make(map[string]func()),
	}
	harness.backoff = newDialBackoff(
		func() time.Time { return harness.now },
		func(delay time.Duration, run func()) func() {
			if delay != stableSession {
				panic("unexpected stable delay")
			}
			harness.timers["last"] = run
			return func() { delete(harness.timers, "last") }
		},
	)
	return harness
}

func TestDialBackoff(t *testing.T) {
	type step struct {
		advance time.Duration
		action  string // begin, failed, up, end, endError, stable, release
		device  string
		wantErr string
	}

	refused := func(remaining string) string {
		return "backing off after failed dial: mqtt: disconnect, next try in " + remaining
	}
	const inProgress = "dial in progress"

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "dial within the cooldown fails fast and names the last error",
			steps: []step{
				{0, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{10 * time.Second, "begin", "cam", refused("20s")},
				{20 * time.Second, "begin", "cam", ""},
			},
		},
		{
			name: "cooldown doubles per failure and caps at ten minutes",
			steps: []step{
				{0, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{30 * time.Second, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{59 * time.Second, "begin", "cam", refused("1s")},
				{time.Second, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{119 * time.Second, "begin", "cam", refused("1s")},
				{time.Second, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{4 * time.Minute, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{8 * time.Minute, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{0, "begin", "cam", refused("10m0s")},
				{10 * time.Minute, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{0, "begin", "cam", refused("10m0s")},
			},
		},
		{
			name: "a second dial while one is in flight fails fast",
			steps: []step{
				{0, "begin", "cam", ""},
				{0, "begin", "cam", inProgress},
				{0, "begin", "other", ""},
				{0, "release", "cam", ""},
				{0, "begin", "cam", ""},
			},
		},
		{
			name: "devices do not share a cooldown",
			steps: []step{
				{0, "begin", "cam-a", ""},
				{0, "failed", "cam-a", ""},
				{0, "begin", "cam-a", refused("30s")},
				{0, "begin", "cam-b", ""},
			},
		},
		{
			name: "a session that drops before it is stable counts as a failure",
			steps: []step{
				{0, "begin", "cam", ""},
				{0, "up", "cam", ""},
				{5 * time.Second, "endError", "cam", ""},
				{0, "begin", "cam", refused("30s")},
			},
		},
		{
			name: "a stable session resets the cooldown",
			steps: []step{
				{0, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{30 * time.Second, "begin", "cam", ""},
				{0, "up", "cam", ""},
				{stableSession, "stable", "cam", ""},
				{0, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{0, "begin", "cam", refused("30s")},
			},
		},
		{
			name: "a normal stop records nothing",
			steps: []step{
				{0, "begin", "cam", ""},
				{0, "up", "cam", ""},
				{5 * time.Second, "end", "cam", ""},
				{0, "begin", "cam", ""},
			},
		},
		{
			name: "a normal stop keeps the earlier failures",
			steps: []step{
				{0, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{30 * time.Second, "begin", "cam", ""},
				{0, "up", "cam", ""},
				{0, "end", "cam", ""},
				{0, "begin", "cam", ""},
				{0, "failed", "cam", ""},
				{0, "begin", "cam", refused("1m0s")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newBackoffHarness()

			for index, step := range test.steps {
				harness.now = harness.now.Add(step.advance)
				session := harness.sessions[step.device]

				switch step.action {
				case "begin":
					began, err := harness.backoff.begin(step.device)
					if step.wantErr == "" && err != nil {
						t.Fatalf("step %d: unexpected error %v", index, err)
					}
					if step.wantErr != "" && (err == nil || err.Error() != step.wantErr) {
						t.Fatalf("step %d: error = %v, want %q", index, err, step.wantErr)
					}
					if err == nil {
						harness.sessions[step.device] = began
					}
				case "failed":
					session.failed(errors.New("mqtt: disconnect"))
				case "release":
					session.release()
				case "up":
					session.up()
				case "end":
					session.end(nil)
				case "endError":
					session.end(errors.New("mqtt: disconnect"))
				case "stable":
					harness.timers["last"]()
				}
			}
		})
	}
}

func TestDialRecordsFailedOemDial(t *testing.T) {
	// Nothing listens on port 443 of the loopback address, so the first dial
	// fails without leaving the machine and the second one must not try again.
	rawURL := "tuya://127.0.0.1?device_id=fake-backoff-device&sid=fake-sid&ecode=fake-ecode" +
		"&partner_identity=fake-partner&app_key=fake-app-key&signing_key=fake-signing-key" +
		"&ch_key=fakech01&package_name=com.fake.app&client_device_id=fake-client-device"

	if _, err := Dial(rawURL); err == nil {
		t.Fatal("first dial succeeded")
	}

	_, err := Dial(rawURL)
	if err == nil {
		t.Fatal("second dial succeeded")
	}

	const prefix = "tuya: backing off after failed dial: "
	if got := err.Error(); len(got) < len(prefix) || got[:len(prefix)] != prefix {
		t.Errorf("second dial error = %q, want prefix %q", got, prefix)
	}
}
