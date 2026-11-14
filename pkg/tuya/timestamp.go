package tuya

import (
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/pion/rtp"
)

const (
	videoClockRate        = 90000
	defaultAudioClockRate = 8000
)

type packetFilter func(packet *rtp.Packet) []*rtp.Packet

// timestampOrigin is shared by all tracks and reconnects, so timestamps of one
// go2rtc process never jump backwards and audio and video share a timeline.
var timestampOrigin = time.Now()

func wallClock() time.Duration {
	return time.Since(timestampOrigin)
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

// newAudioFilter gives every packet a wall clock timestamp. G.711 has one
// sample per byte, so the camera pairs of packets that arrive together are
// spaced by the length of the packet before, as a real sample clock would.
func newAudioFilter(codec *core.Codec, now func() time.Duration) packetFilter {
	clockRate := codec.ClockRate
	if clockRate == 0 {
		clockRate = defaultAudioClockRate
	}
	oneBytePerSample := codec.Name == core.CodecPCMU || codec.Name == core.CodecPCMA

	stamper := &wallClockStamper{now: now, clockRate: clockRate}
	return func(packet *rtp.Packet) []*rtp.Packet {
		step := uint32(1)
		if oneBytePerSample {
			step = uint32(len(packet.Payload))
		}
		packet.Timestamp = stamper.next(step)
		return []*rtp.Packet{packet}
	}
}

// wallClockStamper turns elapsed time into a timestamp that is strictly
// increasing, as consumers need distinct timestamps for distinct frames.
// The step is how far the clock advances for the packet, at least one.
type wallClockStamper struct {
	now       func() time.Duration
	clockRate uint32
	started   bool
	last      uint32
	lastStep  uint32
}

func (s *wallClockStamper) next(step uint32) uint32 {
	timestamp := uint32(s.now().Seconds() * float64(s.clockRate))
	if s.started && int32(timestamp-(s.last+s.lastStep)) < 0 {
		timestamp = s.last + s.lastStep
	}

	s.started = true
	s.last = timestamp
	s.lastStep = max(step, 1)
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
		s.current = s.clock.next(1)
		s.newFrame = false
	}

	packet.Timestamp = s.current
	if packet.Marker && !isNonVclNalu(packet.Payload) {
		s.newFrame = true
	}
}

// isNonVclNalu reports single SEI, SPS, PPS and AUD NALs. Some cameras mark
// them, which would end the frame before its slices, as in pkg/h264/rtp.go.
func isNonVclNalu(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}

	switch payload[0] & 0x1F {
	case h264.NALUTypeSEI, h264.NALUTypeSPS, h264.NALUTypePPS, h264.NALUTypeAUD:
		return true
	}
	return false
}
