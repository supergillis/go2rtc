package tuya

import (
	"reflect"
	"testing"

	"github.com/pion/rtp"
)

func TestParameterSetInjector(t *testing.T) {
	sps := []byte{0x67, 1, 2}
	pps := []byte{0x68, 3}
	stap := []byte{0x18, 0, 3, 0x67, 1, 2, 0, 2, 0x68, 3}
	idr := []byte{0x65, 5}
	fuStart := []byte{0x7c, 0x85, 6}
	fuMiddle := []byte{0x7c, 0x05, 7}
	pFrame := []byte{0x41, 4}

	// Each input gets its own sequence number and timestamp, so the output shows which header was used.
	input := func(seq uint16, payload []byte) *rtp.Packet {
		return &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: uint32(seq) * 3000, SSRC: 7, Marker: true},
			Payload: payload,
		}
	}
	// Injected packets reuse the header of the keyframe packet, without the marker.
	injected := func(seq uint16, payload []byte) *rtp.Packet {
		packet := input(seq, payload)
		packet.Marker = false
		return packet
	}

	tests := []struct {
		name  string
		input []*rtp.Packet
		want  [][]*rtp.Packet
	}{
		{
			name:  "idr before any parameter sets passes alone",
			input: []*rtp.Packet{input(1, idr)},
			want:  [][]*rtp.Packet{{input(1, idr)}},
		},
		{
			name:  "single nal sps and pps are cached and sent before idr",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, idr)},
			want: [][]*rtp.Packet{
				{input(1, sps)},
				{input(2, pps)},
				{injected(3, sps), injected(3, pps), input(3, idr)},
			},
		},
		{
			name:  "stap-a parameter sets are cached and sent as single nals before idr",
			input: []*rtp.Packet{input(1, stap), input(2, idr)},
			want: [][]*rtp.Packet{
				{input(1, stap)},
				{injected(2, sps), injected(2, pps), input(2, idr)},
			},
		},
		{
			name:  "fu-a start of an idr gets the parameter sets, middle fragments do not",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, fuStart), input(4, fuMiddle)},
			want: [][]*rtp.Packet{
				{input(1, sps)},
				{input(2, pps)},
				{injected(3, sps), injected(3, pps), input(3, fuStart)},
				{input(4, fuMiddle)},
			},
		},
		{
			name:  "p frame passes alone",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, pFrame)},
			want: [][]*rtp.Packet{
				{input(1, sps)},
				{input(2, pps)},
				{input(3, pFrame)},
			},
		},
		{
			name:  "every idr gets the parameter sets",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, idr), input(4, pFrame), input(5, idr)},
			want: [][]*rtp.Packet{
				{input(1, sps)},
				{input(2, pps)},
				{injected(3, sps), injected(3, pps), input(3, idr)},
				{input(4, pFrame)},
				{injected(5, sps), injected(5, pps), input(5, idr)},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			injector := newParameterSetInjector()

			got := make([][]*rtp.Packet, 0, len(test.input))
			for _, packet := range test.input {
				got = append(got, injector.process(packet))
			}

			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("process output = %+v, want %+v", got, test.want)
			}
		})
	}
}
