package tuya

import (
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
)

const videoClockRate = 90000

// timestampOrigin is shared by all tracks and reconnects, so timestamps of one
// go2rtc process never jump backwards and audio and video share a timeline.
var timestampOrigin = time.Now()

// stamper replaces the timestamps of a track whose camera does not advance them.
// Some cameras, such as the Philips Avent, send zero on every packet. A camera
// with advancing timestamps is never touched.
type stamper struct {
	clockRate uint32

	// The source timestamp of the previous frame, to see that it does not advance.
	seen   bool
	source uint32
	// stalled stays true for the rest of the session once it is set.
	stalled bool

	started bool
	next    uint32
	last    uint32

	// The highest sequence number, to see late and retransmitted packets.
	highest      uint16
	sequenceSeen bool
}

// late reports a packet that is not newer than the highest one seen. Such a
// packet belongs to a frame that is already over, so it must neither start a
// frame nor feed the stall detection.
func (s *stamper) late(sequence uint16) bool {
	if s.sequenceSeen && int16(sequence-s.highest) <= 0 {
		return true
	}

	s.sequenceSeen = true
	s.highest = sequence
	return false
}

// stallsAt takes the source timestamp of a new frame and reports whether the
// track must be stamped from this frame on. That is when the first frame has
// timestamp zero, or when a frame repeats the timestamp of the one before.
func (s *stamper) stallsAt(source uint32) bool {
	// RFC 3550 starts timestamps at a random value, so a first timestamp of zero
	// is a reliable sign of a camera that does not set them.
	if (!s.seen && source == 0) || (s.seen && source == s.source) {
		s.stalled = true
	}

	s.seen = true
	s.source = source
	return s.stalled
}

// stamp returns a wall clock timestamp that is strictly increasing, as consumers
// need distinct timestamps for distinct frames. The clock advances by step for the
// packet, at least one.
func (s *stamper) stamp(step uint32) uint32 {
	// Whole seconds and the rest are scaled apart, so the uint32 wraps exactly
	// and a long uptime never overflows the duration product.
	elapsed := time.Since(timestampOrigin)
	timestamp := uint32(elapsed/time.Second)*s.clockRate + uint32(elapsed%time.Second*time.Duration(s.clockRate)/time.Second)
	if s.started && int32(timestamp-s.next) < 0 {
		timestamp = s.next
	}

	s.started = true
	s.next = timestamp + max(step, 1)
	s.last = timestamp
	return timestamp
}

// stampVideo gives all packets of a frame one timestamp once the camera timestamps
// stall. Packets pass as they are until then. A frame ends after a marked VCL packet.
func stampVideo(next core.HandlerFunc) core.HandlerFunc {
	stamper := stamper{clockRate: videoClockRate}
	var frameOpen bool

	return func(packet *core.Packet) {
		late := stamper.late(packet.SequenceNumber)
		if !late {
			if !frameOpen {
				frameOpen = true
				if stamper.stallsAt(packet.Timestamp) {
					stamper.stamp(1)
				}
			}
			frameOpen = !packet.Marker || isNonVclNalu(packet.Payload)
		}

		if !stamper.stalled {
			next(packet)
			return
		}

		stamped := copyPacket(packet)
		stamped.Timestamp = stamper.last
		next(stamped)
	}
}

// stampAudio gives every packet a wall clock timestamp once the camera timestamps
// stall. G.711 has one sample per byte, so packets that arrive together are spaced
// by the length of the packet before, as a real sample clock would.
func stampAudio(codec *core.Codec, next core.HandlerFunc) core.HandlerFunc {
	stamper := stamper{clockRate: codec.ClockRate}
	oneBytePerSample := codec.Name == core.CodecPCMU || codec.Name == core.CodecPCMA

	return func(packet *core.Packet) {
		if stamper.late(packet.SequenceNumber) {
			if !stamper.stalled {
				next(packet)
				return
			}

			stamped := copyPacket(packet)
			stamped.Timestamp = stamper.last
			next(stamped)
			return
		}

		if !stamper.stallsAt(packet.Timestamp) {
			next(packet)
			return
		}

		step := uint32(1)
		if oneBytePerSample {
			step = uint32(len(packet.Payload))
		}

		stamped := copyPacket(packet)
		stamped.Timestamp = stamper.stamp(step)
		next(stamped)
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
