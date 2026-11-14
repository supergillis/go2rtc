package tuya

import (
	"time"

	"github.com/pion/rtp"
)

const (
	videoClockRate        = 90000
	defaultAudioClockRate = 8000
)

type packetFilter func(packet *rtp.Packet) []*rtp.Packet

// newWallClock returns the time elapsed since the first call of the clock.
func newWallClock() func() time.Duration {
	var start time.Time
	return func() time.Duration {
		if start.IsZero() {
			start = time.Now()
		}
		return time.Since(start)
	}
}

// newVideoFilter injects parameter sets and, when a clock is given, rewrites the
// timestamps. The OEM camera sends a timestamp of zero on every packet.
func newVideoFilter(now func() time.Duration) packetFilter {
	injector := newParameterSetInjector()
	if now == nil {
		return injector.process
	}

	stamper := &frameStamper{now: now, newFrame: true}
	return func(packet *rtp.Packet) []*rtp.Packet {
		stamper.stamp(packet)
		return injector.process(packet)
	}
}

// newAudioFilter gives every packet its own wall clock timestamp.
func newAudioFilter(clockRate uint32, now func() time.Duration) packetFilter {
	if clockRate == 0 {
		clockRate = defaultAudioClockRate
	}

	stamper := &wallClockStamper{now: now, clockRate: clockRate}
	return func(packet *rtp.Packet) []*rtp.Packet {
		packet.Timestamp = stamper.next()
		return []*rtp.Packet{packet}
	}
}

// wallClockStamper turns elapsed time into a timestamp that starts at zero and
// never goes backwards.
type wallClockStamper struct {
	now       func() time.Duration
	clockRate uint32
	started   bool
	base      time.Duration
	last      uint32
}

func (s *wallClockStamper) next() uint32 {
	elapsed := s.now()
	if !s.started {
		s.started = true
		s.base = elapsed
	}

	timestamp := uint32((elapsed - s.base).Seconds() * float64(s.clockRate))
	if int32(timestamp-s.last) < 0 {
		timestamp = s.last
	}

	s.last = timestamp
	return timestamp
}

// frameStamper takes one timestamp on the first packet after a marker and
// reuses it until the next marker, so all packets of a frame agree.
type frameStamper struct {
	now      func() time.Duration
	newFrame bool
	current  uint32
	clock    *wallClockStamper
}

func (s *frameStamper) stamp(packet *rtp.Packet) {
	if s.clock == nil {
		s.clock = &wallClockStamper{now: s.now, clockRate: videoClockRate}
	}

	if s.newFrame {
		s.current = s.clock.next()
		s.newFrame = false
	}

	packet.Timestamp = s.current
	if packet.Marker {
		s.newFrame = true
	}
}
