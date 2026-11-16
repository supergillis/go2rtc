package tuya

import (
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/pion/rtp"
)

// stampedPacket arrives after the wait since the previous one.
type stampedPacket struct {
	wait      time.Duration
	timestamp uint32
	marker    bool
	payload   []byte
	// sequence is the next one in order when zero.
	sequence uint16
}

func TestStamp(t *testing.T) {
	sps := []byte{0x67, 1, 2}
	idr := []byte{0x65, 5}
	pStart := []byte{0x7c, 0x81, 6}
	pEnd := []byte{0x7c, 0x41, 7}
	g711 := make([]byte, 160)
	frame := 40 * time.Millisecond
	audioFrame := 20 * time.Millisecond

	pcmu := &core.Codec{Name: core.CodecPCMU, ClockRate: 8000}

	tests := []struct {
		name  string
		audio *core.Codec
		input []stampedPacket
		want  []uint32
	}{
		{
			name:  "a first timestamp of zero is stamped from the first frame",
			input: []stampedPacket{{frame, 0, true, pEnd, 0}, {frame, 0, true, pEnd, 0}, {frame, 0, true, pEnd, 0}},
			want:  []uint32{3600, 7200, 10800},
		},
		{
			name:  "a non-zero start that stalls later is stamped from the repeat",
			input: []stampedPacket{{0, 1000, true, pEnd, 0}, {frame, 4600, true, pEnd, 0}, {frame, 4600, true, pEnd, 0}, {frame, 4600, true, pEnd, 0}},
			want:  []uint32{1000, 4600, 7200, 10800},
		},
		{
			name: "a late packet of a finished frame changes nothing",
			input: []stampedPacket{
				{0, 1000, false, pStart, 1}, {0, 1000, true, pEnd, 3}, {0, 1000, true, pEnd, 2},
				{frame, 4000, false, pStart, 4}, {0, 4000, true, pEnd, 5},
			},
			want: []uint32{1000, 1000, 1000, 4000, 4000},
		},
		{
			name:  "advancing timestamps pass through",
			input: []stampedPacket{{0, 1000, true, pEnd, 0}, {frame, 4600, true, pEnd, 0}, {frame, 8200, true, pEnd, 0}},
			want:  []uint32{1000, 4600, 8200},
		},
		{
			name: "all packets of a frame share one timestamp",
			input: []stampedPacket{
				{0, 0, false, pStart, 0}, {0, 0, true, pEnd, 0},
				{frame, 0, false, pStart, 0}, {0, 0, true, pEnd, 0},
				{frame, 0, false, pStart, 0}, {0, 0, true, pEnd, 0},
			},
			want: []uint32{0, 0, 3600, 3600, 7200, 7200},
		},
		{
			name:  "a marked sps does not end the frame",
			input: []stampedPacket{{0, 0, true, pEnd, 0}, {frame, 0, true, sps, 0}, {0, 0, true, idr, 0}, {frame, 0, true, pEnd, 0}},
			want:  []uint32{0, 3600, 3600, 7200},
		},
		{
			name: "a stalled clock stays strictly increasing",
			input: []stampedPacket{
				{0, 0, true, pEnd, 0}, {frame, 0, true, pEnd, 0}, {0, 0, true, pEnd, 0}, {0, 0, true, pEnd, 0},
			},
			want: []uint32{0, 3600, 3601, 3602},
		},
		{
			name:  "g711 packets are 20 ms apart",
			audio: pcmu,
			input: []stampedPacket{{0, 0, true, g711, 0}, {audioFrame, 0, true, g711, 0}, {audioFrame, 0, true, g711, 0}},
			want:  []uint32{0, 160, 320},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				timestampOrigin = time.Now()

				var got []uint32
				collect := func(packet *core.Packet) { got = append(got, packet.Timestamp) }

				stamp := stampVideo(collect)
				if test.audio != nil {
					stamp = stampAudio(test.audio, collect)
				}

				for index, item := range test.input {
					time.Sleep(item.wait)
					sequence := item.sequence
					if sequence == 0 {
						sequence = uint16(index + 1)
					}
					stamp(&rtp.Packet{
						Header:  rtp.Header{SequenceNumber: sequence, Timestamp: item.timestamp, Marker: item.marker},
						Payload: item.payload,
					})
				}

				if !reflect.DeepEqual(got, test.want) {
					t.Errorf("timestamps = %v, want %v", got, test.want)
				}
			})
		})
	}
}
