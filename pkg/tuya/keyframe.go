package tuya

import (
	"encoding/binary"
	"slices"

	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/pion/rtp"
)

const (
	nalTypeStapA = 24
	nalTypeFuA   = 28
	fuStartBit   = 0x80
)

// parameterSetInjector repeats the cached SPS and PPS before every keyframe.
// The camera sends them rarely and go2rtc sends no PLI to active producers, so a
// consumer that joins a running stream would otherwise never decode.
// Injected packets shift the sequence numbers after them, so the output stays
// strictly consecutive and jitter buffers do not drop duplicates.
type parameterSetInjector struct {
	sps    []byte
	pps    []byte
	offset uint16
}

func newParameterSetInjector() *parameterSetInjector {
	return &parameterSetInjector{}
}

// process returns the packets to forward, the input packet last.
func (i *parameterSetInjector) process(packet *rtp.Packet) []*rtp.Packet {
	if len(packet.Payload) > 0 {
		switch packet.Payload[0] & 0x1F {
		case h264.NALUTypeSPS, h264.NALUTypePPS:
			i.cache(packet.Payload)
		case nalTypeStapA:
			i.cacheStapA(packet.Payload[1:])
		case h264.NALUTypeIFrame:
			return i.withParameterSets(packet)
		case nalTypeFuA:
			// Only the first fragment of an IDR needs them, the rest continues it.
			if len(packet.Payload) > 1 && packet.Payload[1]&fuStartBit != 0 &&
				packet.Payload[1]&0x1F == h264.NALUTypeIFrame {
				return i.withParameterSets(packet)
			}
		}
	}

	return []*rtp.Packet{i.shifted(packet, nil)}
}

func (i *parameterSetInjector) withParameterSets(packet *rtp.Packet) []*rtp.Packet {
	if i.sps == nil || i.pps == nil {
		return []*rtp.Packet{i.shifted(packet, nil)}
	}

	out := make([]*rtp.Packet, 0, 3)
	for _, payload := range [][]byte{i.sps, i.pps} {
		injected := i.shifted(packet, payload)
		injected.Marker = false
		out = append(out, injected)
		i.offset++
	}
	return append(out, i.shifted(packet, nil))
}

// shifted copies only the header, with the running sequence offset applied.
// A nil payload keeps the payload of the packet.
func (i *parameterSetInjector) shifted(packet *rtp.Packet, payload []byte) *rtp.Packet {
	if payload == nil {
		payload = packet.Payload
	}

	header := packet.Header.Clone()
	header.SequenceNumber += i.offset
	return &rtp.Packet{Header: header, Payload: payload}
}

func (i *parameterSetInjector) cache(nalu []byte) {
	switch nalu[0] & 0x1F {
	case h264.NALUTypeSPS:
		i.sps = slices.Clone(nalu)
	case h264.NALUTypePPS:
		i.pps = slices.Clone(nalu)
	}
}

func (i *parameterSetInjector) cacheStapA(payload []byte) {
	for len(payload) > 2 {
		size := int(binary.BigEndian.Uint16(payload))
		payload = payload[2:]
		if size == 0 || size > len(payload) {
			return
		}
		i.cache(payload[:size])
		payload = payload[size:]
	}
}
