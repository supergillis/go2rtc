package tuya

import (
	"reflect"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// fakeClock returns the given elapsed times one by one.
func fakeClock(elapsed ...time.Duration) func() time.Duration {
	return func() time.Duration {
		next := elapsed[0]
		elapsed = elapsed[1:]
		return next
	}
}

type timestampPacket struct {
	sequence  uint16
	timestamp uint32
	marker    bool
	payload   []byte
}

func toPackets(items []timestampPacket) []*rtp.Packet {
	packets := make([]*rtp.Packet, 0, len(items))
	for _, item := range items {
		packets = append(packets, &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: item.sequence,
				Timestamp: item.timestamp, SSRC: 7, Marker: item.marker,
			},
			Payload: item.payload,
		})
	}
	return packets
}

func TestVideoFilterTimestamps(t *testing.T) {
	sps := []byte{0x67, 1, 2}
	pps := []byte{0x68, 3}
	idr := []byte{0x65, 5}
	pStart := []byte{0x7c, 0x81, 6}
	pEnd := []byte{0x7c, 0x41, 7}

	tests := []struct {
		name  string
		clock func() time.Duration
		input []timestampPacket
		want  [][]timestampPacket
	}{
		{
			name:  "camera timestamps of zero become wall clock per frame",
			clock: fakeClock(5*time.Second, 5*time.Second+40*time.Millisecond, 5*time.Second+80*time.Millisecond),
			input: []timestampPacket{
				{1, 0, false, pStart}, {2, 0, true, pEnd},
				{3, 0, true, pEnd},
				{4, 0, true, pEnd},
			},
			want: [][]timestampPacket{
				{{1, 0, false, pStart}},
				{{2, 0, true, pEnd}},
				{{3, 3600, true, pEnd}},
				{{4, 7200, true, pEnd}},
			},
		},
		{
			name:  "injected parameter sets share the keyframe timestamp",
			clock: fakeClock(time.Second, time.Second+3900*time.Millisecond),
			input: []timestampPacket{
				{1, 0, false, sps}, {2, 0, false, pps}, {3, 0, true, idr},
				{4, 0, false, sps}, {5, 0, false, pps}, {6, 0, true, idr},
			},
			want: [][]timestampPacket{
				{{1, 0, false, sps}},
				{{2, 0, false, pps}},
				{{3, 0, false, sps}, {4, 0, false, pps}, {5, 0, true, idr}},
				{{6, 351000, false, sps}},
				{{7, 351000, false, pps}},
				{{8, 351000, false, sps}, {9, 351000, false, pps}, {10, 351000, true, idr}},
			},
		},
		{
			name:  "timestamps never go backwards",
			clock: fakeClock(time.Second, time.Second+40*time.Millisecond, time.Second+20*time.Millisecond, time.Second+80*time.Millisecond),
			input: []timestampPacket{
				{1, 0, true, pEnd}, {2, 0, true, pEnd}, {3, 0, true, pEnd}, {4, 0, true, pEnd},
			},
			want: [][]timestampPacket{
				{{1, 0, true, pEnd}},
				{{2, 3600, true, pEnd}},
				{{3, 3600, true, pEnd}},
				{{4, 7200, true, pEnd}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter := newVideoFilter(test.clock)

			got := make([][]*rtp.Packet, 0, len(test.input))
			for _, packet := range toPackets(test.input) {
				got = append(got, filter(packet))
			}

			want := make([][]*rtp.Packet, 0, len(test.want))
			for _, items := range test.want {
				want = append(want, toPackets(items))
			}

			if !reflect.DeepEqual(got, want) {
				t.Errorf("timestamps = %v, want %v", timestamps(got), timestamps(want))
			}
		})
	}
}

func timestamps(output [][]*rtp.Packet) [][]uint32 {
	result := make([][]uint32, 0, len(output))
	for _, packets := range output {
		row := make([]uint32, 0, len(packets))
		for _, packet := range packets {
			row = append(row, packet.Timestamp)
		}
		result = append(result, row)
	}
	return result
}

func TestAudioFilterTimestamps(t *testing.T) {
	payload := []byte{1, 2}

	tests := []struct {
		name      string
		clockRate uint32
		clock     func() time.Duration
		want      []uint32
	}{
		{
			name:      "8 kHz",
			clockRate: 8000,
			clock:     fakeClock(2*time.Second, 2*time.Second+20*time.Millisecond, 2*time.Second+40*time.Millisecond),
			want:      []uint32{0, 160, 320},
		},
		{
			name:      "16 kHz",
			clockRate: 16000,
			clock:     fakeClock(2*time.Second, 2*time.Second+20*time.Millisecond, 2*time.Second+40*time.Millisecond),
			want:      []uint32{0, 320, 640},
		},
		{
			name:      "never goes backwards",
			clockRate: 8000,
			clock:     fakeClock(time.Second, time.Second+20*time.Millisecond, time.Second+10*time.Millisecond),
			want:      []uint32{0, 160, 160},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter := newAudioFilter(test.clockRate, test.clock)

			got := make([]uint32, 0, len(test.want))
			for sequence := range test.want {
				out := filter(toPackets([]timestampPacket{{uint16(sequence), 0, true, payload}})[0])
				if len(out) != 1 || out[0].SequenceNumber != uint16(sequence) {
					t.Fatalf("filter output = %+v, want the single input packet", out)
				}
				got = append(got, out[0].Timestamp)
			}

			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("timestamps = %v, want %v", got, test.want)
			}
		})
	}
}
