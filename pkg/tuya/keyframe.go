package tuya

import (
	"encoding/binary"
	"slices"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
)

const (
	nalTypeStapA = 24
	nalTypeFuA   = 28
	fuStartBit   = 0x80
)

// repeatInterval is how long the parameter sets may stay unsent, in 90 kHz ticks.
const repeatInterval = 90000

// injectParameterSets repeats the cached SPS and PPS before every keyframe and
// before the first packet of a frame once a second has passed without them.
// The camera sends them rarely and go2rtc sends no PLI to active producers, so a
// consumer that joins a running stream would otherwise never decode. At night the
// camera sends a keyframe only every 7 s, longer than the probe of ffmpeg.
// The clock is the RTP timestamp, so stamping must run first.
// Injected packets shift the sequence numbers after them, so the output stays
// consecutive and jitter buffers do not drop duplicates. Packets passed in are
// never modified, a changed packet is a copy with its own header.
func injectParameterSets(next core.HandlerFunc) core.HandlerFunc {
	var sps, pps []byte
	var shift uint16
	var sentAt uint32
	frameStart := true

	cache := func(nalu []byte, timestamp uint32) {
		switch nalu[0] & 0x1F {
		case h264.NALUTypeSPS:
			sps = slices.Clone(nalu)
			sentAt = timestamp
		case h264.NALUTypePPS:
			pps = slices.Clone(nalu)
			sentAt = timestamp
		}
	}

	return func(packet *core.Packet) {
		payload := packet.Payload
		inject := false

		switch {
		case len(payload) == 0:
		case payload[0]&0x1F == nalTypeStapA:
			for units := payload[1:]; len(units) > 2; {
				size := int(binary.BigEndian.Uint16(units))
				units = units[2:]
				if size == 0 || size > len(units) {
					break
				}
				cache(units[:size], packet.Timestamp)
				units = units[size:]
			}
		case isParameterSet(payload):
			cache(payload, packet.Timestamp)
		default:
			inject = sps != nil && pps != nil &&
				(startsKeyframe(payload) || frameStart && int32(packet.Timestamp-sentAt) >= repeatInterval)
		}

		frameStart = packet.Marker

		if inject {
			sentAt = packet.Timestamp
			for _, parameterSet := range [][]byte{sps, pps} {
				injected := copyPacket(packet)
				injected.Payload = parameterSet
				injected.Marker = false
				injected.SequenceNumber += shift
				next(injected)
				shift++
			}
		}

		if shift == 0 {
			next(packet)
			return
		}

		shifted := copyPacket(packet)
		shifted.SequenceNumber += shift
		next(shifted)
	}
}

// copyPacket copies the header, so the copy can change without touching the
// packet of the sender. The payload is shared and never written.
func copyPacket(packet *core.Packet) *core.Packet {
	copied := *packet
	copied.Header = packet.Header.Clone()
	return &copied
}

func isParameterSet(payload []byte) bool {
	nalType := payload[0] & 0x1F
	return nalType == h264.NALUTypeSPS || nalType == h264.NALUTypePPS
}

// startsKeyframe is true for a single NAL IDR and for the first fragment of an
// FU-A IDR, the later fragments continue it.
func startsKeyframe(payload []byte) bool {
	switch payload[0] & 0x1F {
	case h264.NALUTypeIFrame:
		return true
	case nalTypeFuA:
		return len(payload) > 1 && payload[1]&fuStartBit != 0 && payload[1]&0x1F == h264.NALUTypeIFrame
	}
	return false
}
