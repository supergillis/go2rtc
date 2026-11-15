package tuya

import (
	"fmt"
	"sync"
	"time"
)

const (
	minDialCooldown = 30 * time.Second
	maxDialCooldown = 10 * time.Minute
)

// oemDialBackoff is shared by all streams. A camera has only a few session
// slots and frees an abandoned one after about 20 minutes, so every retry
// of a refused dial would keep the slots full.
var oemDialBackoff = newDialBackoff(time.Now)

type dialBackoff struct {
	mu      sync.Mutex
	now     func() time.Time
	devices map[string]*dialFailures
}

type dialFailures struct {
	count   int
	retryAt time.Time
}

func newDialBackoff(now func() time.Time) *dialBackoff {
	return &dialBackoff{now: now, devices: make(map[string]*dialFailures)}
}

// check returns an error while the device is cooling down.
func (b *dialBackoff) check(deviceId string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	failures, ok := b.devices[deviceId]
	if !ok {
		return nil
	}

	if remaining := failures.retryAt.Sub(b.now()); remaining > 0 {
		return fmt.Errorf("camera refused recently, next try in %s", remaining.Round(time.Second))
	}
	return nil
}

// record stores the outcome of a dial. A success clears the cooldown.
func (b *dialBackoff) record(deviceId string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err == nil {
		delete(b.devices, deviceId)
		return
	}

	failures, ok := b.devices[deviceId]
	if !ok {
		failures = &dialFailures{}
		b.devices[deviceId] = failures
	}

	cooldown := maxDialCooldown
	if failures.count < 6 {
		cooldown = min(minDialCooldown<<failures.count, maxDialCooldown)
	}

	failures.count++
	failures.retryAt = b.now().Add(cooldown)
}
