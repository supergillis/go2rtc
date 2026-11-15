package tuya

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	maxBackoff = 10 * time.Minute

	// stableSession is how long a session must stay up to count as a success.
	// The camera evicts a session soon after it connects when its slots are full.
	stableSession = 60 * time.Second
)

// oemDialBackoff is shared by all streams. A camera has only a few session
// slots and frees an abandoned one after about 20 minutes, so every retry
// of a refused dial would keep the slots full.
var oemDialBackoff = &backoff{minDelay: 30 * time.Second}

// oemOffBackoff spaces the power checks of a camera that is off. It is separate
// from the refusals, so an off camera never slows a real retry.
var oemOffBackoff = &backoff{minDelay: 60 * time.Second}

var errInFlight = errors.New("dial in progress")

// backoff holds one attempt per camera at a time and doubles the wait after
// each failure.
type backoff struct {
	minDelay time.Duration

	mu      sync.Mutex
	cameras map[string]*cameraBackoff
}

type cameraBackoff struct {
	failures int
	retryAt  time.Time
	lastErr  error
	inFlight bool
}

// acquire reserves the camera for one attempt. It fails while the camera cools
// down after a failure, or while another attempt is in flight.
func (b *backoff) acquire(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	camera := b.cameras[id]
	if camera == nil {
		camera = &cameraBackoff{}
		if b.cameras == nil {
			b.cameras = make(map[string]*cameraBackoff)
		}
		b.cameras[id] = camera
	}

	if camera.inFlight {
		return errInFlight
	}

	if remaining := time.Until(camera.retryAt); remaining > 0 {
		return fmt.Errorf("%w, next try in %s", camera.lastErr, remaining.Round(time.Second))
	}

	camera.inFlight = true
	return nil
}

// release frees the camera. An error counts as a failure, without one the
// attempt has no verdict and the schedule stays as it was.
func (b *backoff) release(id string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	camera := b.cameras[id]
	if camera == nil {
		return
	}

	camera.inFlight = false

	if err != nil {
		camera.retryAt = time.Now().Add(min(b.minDelay<<min(camera.failures, 6), maxBackoff))
		camera.failures++
		camera.lastErr = err
	} else if camera.failures == 0 {
		delete(b.cameras, id)
	}
}

// reset frees the camera and forgets its failures.
func (b *backoff) reset(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.cameras, id)
}
