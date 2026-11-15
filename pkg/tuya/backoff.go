package tuya

import (
	"fmt"
	"sync"
	"time"
)

const (
	minDialCooldown = 30 * time.Second
	maxDialCooldown = 10 * time.Minute
	// stableSession is how long a session must stay up to count as a success.
	// The camera evicts a session soon after it connects when its slots are full.
	stableSession = 60 * time.Second
)

// oemDialBackoff is shared by all streams. A camera has only a few session
// slots and frees an abandoned one after about 20 minutes, so every retry
// of a refused dial would keep the slots full.
var oemDialBackoff = newDialBackoff(time.Now, func(delay time.Duration, run func()) func() {
	timer := time.AfterFunc(delay, run)
	return func() { timer.Stop() }
})

type dialBackoff struct {
	mu        sync.Mutex
	now       func() time.Time
	afterFunc func(delay time.Duration, run func()) (stop func())
	devices   map[string]*deviceDials
}

type deviceDials struct {
	failures int
	retryAt  time.Time
	lastErr  error
	inFlight bool
}

func newDialBackoff(now func() time.Time, afterFunc func(time.Duration, func()) func()) *dialBackoff {
	return &dialBackoff{now: now, afterFunc: afterFunc, devices: make(map[string]*deviceDials)}
}

// begin reserves the device for one dial. It fails while the device cools down
// after a failure, or while another dial for it is in flight.
func (b *dialBackoff) begin(deviceId string) (*dialSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	device := b.devices[deviceId]
	if device == nil {
		device = &deviceDials{}
		b.devices[deviceId] = device
	}

	if device.inFlight {
		return nil, fmt.Errorf("dial in progress")
	}

	if remaining := device.retryAt.Sub(b.now()); remaining > 0 {
		return nil, fmt.Errorf("backing off after failed dial: %w, next try in %s", device.lastErr, remaining.Round(time.Second))
	}

	device.inFlight = true
	return &dialSession{backoff: b, deviceId: deviceId}, nil
}

func (b *dialBackoff) fail(deviceId string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	device := b.devices[deviceId]
	if device == nil {
		device = &deviceDials{}
		b.devices[deviceId] = device
	}

	cooldown := maxDialCooldown
	if device.failures < 6 {
		cooldown = min(minDialCooldown<<device.failures, maxDialCooldown)
	}

	device.failures++
	device.retryAt = b.now().Add(cooldown)
	device.lastErr = err
	device.inFlight = false
}

// release frees the device without a verdict.
func (b *dialBackoff) release(deviceId string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	device := b.devices[deviceId]
	if device == nil {
		return
	}

	device.inFlight = false
	if device.failures == 0 {
		delete(b.devices, deviceId)
	}
}

func (b *dialBackoff) succeed(deviceId string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.devices, deviceId)
}

type dialSessionState int

const (
	sessionDialing dialSessionState = iota
	sessionUp
	sessionDone
)

// dialSession follows one dial and the session it opens. Only a session that
// stays up for stableSession clears the failures of the device.
type dialSession struct {
	backoff  *dialBackoff
	deviceId string

	mu      sync.Mutex
	state   dialSessionState
	pending error
	stop    func()
}

// failed records a dial that returned an error.
func (s *dialSession) failed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == sessionDone {
		return
	}

	s.state = sessionDone
	s.backoff.fail(s.deviceId, err)
}

// release frees the device when the dial ended without a verdict, for example
// on a panic. It does nothing once the dial has been judged.
func (s *dialSession) release() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionDialing {
		return
	}

	s.state = sessionDone
	s.backoff.release(s.deviceId)
}

// up marks the dial as connected and starts the stability timer.
func (s *dialSession) up() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionDialing {
		return
	}

	if s.pending != nil {
		s.state = sessionDone
		s.backoff.fail(s.deviceId, s.pending)
		return
	}

	s.state = sessionUp
	s.backoff.release(s.deviceId)
	s.stop = s.backoff.afterFunc(stableSession, s.stable)
}

func (s *dialSession) stable() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != sessionUp {
		return
	}

	s.state = sessionDone
	s.backoff.succeed(s.deviceId)
}

// end reports how the session finished. A nil error is a normal stop and
// records nothing. An error before the session is stable counts as a failure.
func (s *dialSession) end(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.state {
	case sessionDialing:
		// The dial itself judges this error, unless it already returned.
		if err != nil && s.pending == nil {
			s.pending = err
		}
	case sessionUp:
		s.state = sessionDone
		s.stop()
		if err != nil {
			s.backoff.fail(s.deviceId, err)
		}
	}
}
