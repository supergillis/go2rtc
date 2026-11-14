package tuya

import (
	"reflect"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/webrtc"
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

	// The timestamp follows the input sequence number, so the output shows which header was used.
	header := func(seq uint16, timestampSeq uint16, marker bool) rtp.Header {
		return rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq,
			Timestamp: uint32(timestampSeq) * 3000, SSRC: 7, Marker: marker,
		}
	}
	input := func(seq uint16, payload []byte) *rtp.Packet {
		return &rtp.Packet{Header: header(seq, seq, true), Payload: payload}
	}
	// A forwarded packet keeps its header and gets the shifted sequence number.
	output := func(inputSeq, outputSeq uint16, payload []byte) *rtp.Packet {
		return &rtp.Packet{Header: header(outputSeq, inputSeq, true), Payload: payload}
	}
	// An injected packet takes the timestamp of the keyframe and has no marker.
	injected := func(inputSeq, outputSeq uint16, payload []byte) *rtp.Packet {
		return &rtp.Packet{Header: header(outputSeq, inputSeq, false), Payload: payload}
	}

	tests := []struct {
		name  string
		input []*rtp.Packet
		want  [][]*rtp.Packet
	}{
		{
			name:  "idr before any parameter sets passes alone",
			input: []*rtp.Packet{input(1, idr)},
			want:  [][]*rtp.Packet{{output(1, 1, idr)}},
		},
		{
			name:  "single nal sps and pps are cached and sent before idr",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, idr)},
			want: [][]*rtp.Packet{
				{output(1, 1, sps)},
				{output(2, 2, pps)},
				{injected(3, 3, sps), injected(3, 4, pps), output(3, 5, idr)},
			},
		},
		{
			name:  "stap-a parameter sets are cached and sent as single nals before idr",
			input: []*rtp.Packet{input(1, stap), input(2, idr)},
			want: [][]*rtp.Packet{
				{output(1, 1, stap)},
				{injected(2, 2, sps), injected(2, 3, pps), output(2, 4, idr)},
			},
		},
		{
			name:  "fu-a start of an idr gets the parameter sets, middle fragments do not",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, fuStart), input(4, fuMiddle)},
			want: [][]*rtp.Packet{
				{output(1, 1, sps)},
				{output(2, 2, pps)},
				{injected(3, 3, sps), injected(3, 4, pps), output(3, 5, fuStart)},
				{output(4, 6, fuMiddle)},
			},
		},
		{
			name:  "p frame passes alone",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, pFrame)},
			want: [][]*rtp.Packet{
				{output(1, 1, sps)},
				{output(2, 2, pps)},
				{output(3, 3, pFrame)},
			},
		},
		{
			name:  "every idr gets the parameter sets and the offset grows",
			input: []*rtp.Packet{input(1, sps), input(2, pps), input(3, idr), input(4, pFrame), input(5, idr)},
			want: [][]*rtp.Packet{
				{output(1, 1, sps)},
				{output(2, 2, pps)},
				{injected(3, 3, sps), injected(3, 4, pps), output(3, 5, idr)},
				{output(4, 6, pFrame)},
				{injected(5, 7, sps), injected(5, 8, pps), output(5, 9, idr)},
			},
		},
		{
			name:  "sequence numbers wrap around",
			input: []*rtp.Packet{input(65534, sps), input(65535, pps), input(0, idr), input(1, pFrame)},
			want: [][]*rtp.Packet{
				{output(65534, 65534, sps)},
				{output(65535, 65535, pps)},
				{injected(0, 0, sps), injected(0, 1, pps), output(0, 2, idr)},
				{output(1, 3, pFrame)},
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

func TestClientTrackInjectsParameterSets(t *testing.T) {
	client := &Client{conn: &webrtc.Conn{Mode: core.ModeActiveProducer}}
	media := &core.Media{Kind: core.KindVideo, Direction: core.DirectionRecvonly}
	codec := &core.Codec{Name: core.CodecH264, ClockRate: 90000}

	track, err := client.GetTrack(media, codec)
	if err != nil {
		t.Fatal(err)
	}

	var got [][]byte
	var sequences []uint16
	track.AppendChild(&core.Node{Input: func(packet *rtp.Packet) {
		got = append(got, packet.Payload)
		sequences = append(sequences, packet.SequenceNumber)
	}})

	// pkg/webrtc writes into the receiver it keeps in conn.Receivers.
	source, err := client.conn.GetTrack(media, codec)
	if err != nil {
		t.Fatal(err)
	}

	for seq, payload := range [][]byte{{0x67, 1}, {0x68, 2}, {0x65, 3}} {
		source.WriteRTP(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(seq + 10)}, Payload: payload})
	}

	wantPayloads := [][]byte{{0x67, 1}, {0x68, 2}, {0x67, 1}, {0x68, 2}, {0x65, 3}}
	if !reflect.DeepEqual(got, wantPayloads) {
		t.Errorf("payloads = %v, want %v", got, wantPayloads)
	}

	wantSequences := []uint16{10, 11, 12, 13, 14}
	if !reflect.DeepEqual(sequences, wantSequences) {
		t.Errorf("sequences = %v, want %v", sequences, wantSequences)
	}
}
