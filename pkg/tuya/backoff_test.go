package tuya

import (
	"errors"
	"testing"
	"time"
)

func TestDialBackoff(t *testing.T) {
	type step struct {
		advance time.Duration
		action  string // check, fail or succeed
		device  string
		wantErr string
	}

	refused := func(remaining string) string {
		return "camera refused recently, next try in " + remaining
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "second dial within the cooldown fails fast",
			steps: []step{
				{0, "check", "cam", ""},
				{0, "fail", "cam", ""},
				{10 * time.Second, "check", "cam", refused("20s")},
				{20 * time.Second, "check", "cam", ""},
			},
		},
		{
			name: "cooldown doubles per failure and caps at ten minutes",
			steps: []step{
				{0, "fail", "cam", ""},
				{30 * time.Second, "check", "cam", ""},
				{0, "fail", "cam", ""},
				{59 * time.Second, "check", "cam", refused("1s")},
				{time.Second, "check", "cam", ""},
				{0, "fail", "cam", ""},
				{119 * time.Second, "check", "cam", refused("1s")},
				{time.Second, "fail", "cam", ""},
				{0, "check", "cam", refused("4m0s")},
				{4 * time.Minute, "fail", "cam", ""},
				{0, "check", "cam", refused("8m0s")},
				{8 * time.Minute, "fail", "cam", ""},
				{0, "check", "cam", refused("10m0s")},
				{10 * time.Minute, "fail", "cam", ""},
				{0, "check", "cam", refused("10m0s")},
			},
		},
		{
			name: "a success resets the cooldown",
			steps: []step{
				{0, "fail", "cam", ""},
				{30 * time.Second, "fail", "cam", ""},
				{60 * time.Second, "succeed", "cam", ""},
				{0, "check", "cam", ""},
				{0, "fail", "cam", ""},
				{0, "check", "cam", refused("30s")},
			},
		},
		{
			name: "devices do not share a cooldown",
			steps: []step{
				{0, "fail", "cam-a", ""},
				{0, "check", "cam-a", refused("30s")},
				{0, "check", "cam-b", ""},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
			backoff := newDialBackoff(func() time.Time { return now })

			for index, step := range test.steps {
				now = now.Add(step.advance)

				switch step.action {
				case "check":
					err := backoff.check(step.device)
					if step.wantErr == "" && err != nil {
						t.Fatalf("step %d: unexpected error %v", index, err)
					}
					if step.wantErr != "" && (err == nil || err.Error() != step.wantErr) {
						t.Fatalf("step %d: error = %v, want %q", index, err, step.wantErr)
					}
				case "fail":
					backoff.record(step.device, errors.New("mqtt: disconnect"))
				case "succeed":
					backoff.record(step.device, nil)
				}
			}
		})
	}
}
