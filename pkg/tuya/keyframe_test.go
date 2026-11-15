package tuya

import (
	"reflect"
	"testing"

	"github.com/pion/rtp"
)

func TestInjectParameterSets(t *testing.T) {
	sps := []byte{0x67, 1, 2}
	pps := []byte{0x68, 3}
	stap := []byte{0x18, 0, 3, 0x67, 1, 2, 0, 2, 0x68, 3}
	idr := []byte{0x65, 5}
	fuStart := []byte{0x7c, 0x85, 6}
	fuMiddle := []byte{0x7c, 0x05, 7}
	pFrame := []byte{0x41, 4}

	// The timestamp follows the input sequence number, so the output shows which header was used.
	packet := func(sequence, timestampSequence uint16, marker bool, payload []byte) *rtp.Packet {
		return &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: sequence,
				Timestamp: uint32(timestampSequence) * 3000, SSRC: 7, Marker: marker,
			},
			Payload: payload,
		}
	}
	input := func(sequence uint16, payload []byte) *rtp.Packet {
		return packet(sequence, sequence, true, payload)
	}
	// A forwarded packet keeps its header and gets the shifted sequence number.
	output := func(inputSequence, outputSequence uint16, payload []byte) *rtp.Packet {
		return packet(outputSequence, inputSequence, true, payload)
	}
	// An injected packet takes the timestamp of the keyframe and has no marker.
	injected := func(inputSequence, outputSequence uint16, payload []byte) *rtp.Packet {
		return packet(outputSequence, inputSequence, false, payload)
	}

	tests := []struct {
		name  string
		input []*rtp.Packet
		want  []*rtp.Packet
	}{
		{
			name:  "single nal sps and pps are cached and sent before idr",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, idr)},
			want: []*rtp.Packet{
				output(1, 1, sps), output(2, 2, pps),
				injected(3, 3, sps), injected(3, 4, pps), output(3, 5, idr),
			},
		},
		{
			name:  "stap-a parameter sets are cached and sent as single nals before idr",
			input: []*rtp.Packet{input(1, stap), input(2, idr)},
			want: []*rtp.Packet{
				output(1, 1, stap),
				injected(2, 2, sps), injected(2, 3, pps), output(2, 4, idr),
			},
		},
		{
			name:  "fu-a start of an idr gets the parameter sets, middle fragments do not",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, fuStart), input(4, fuMiddle)},
			want: []*rtp.Packet{
				output(1, 1, sps), output(2, 2, pps),
				injected(3, 3, sps), injected(3, 4, pps), output(3, 5, fuStart),
				output(4, 6, fuMiddle),
			},
		},
		{
			name: "frames half a second apart get one repeat per second and none mid-frame",
			input: []*rtp.Packet{
				input(1, sps), input(2, pps), input(17, pFrame), input(32, pFrame),
				packet(47, 47, false, pFrame), packet(77, 77, false, pFrame), input(78, pFrame), input(79, pFrame),
			},
			want: []*rtp.Packet{
				output(1, 1, sps), output(2, 2, pps), output(17, 17, pFrame),
				injected(32, 32, sps), injected(32, 33, pps), output(32, 34, pFrame),
				packet(49, 47, false, pFrame), packet(79, 77, false, pFrame), output(78, 80, pFrame),
				injected(79, 81, sps), injected(79, 82, pps), output(79, 83, pFrame),
			},
		},
		{
			name:  "sequence numbers wrap around",
			input: []*rtp.Packet{input(65534, sps), input(65535, pps), input(0, idr), input(1, pFrame)},
			want: []*rtp.Packet{
				output(65534, 65534, sps), output(65535, 65535, pps),
				injected(0, 0, sps), injected(0, 1, pps), output(0, 2, idr),
				output(1, 3, pFrame),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []*rtp.Packet
			handler := injectParameterSets(func(packet *rtp.Packet) {
				got = append(got, packet)
			})

			for _, packet := range test.input {
				handler(packet)
			}

			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("packets = %+v, want %+v", got, test.want)
			}
		})
	}
}
