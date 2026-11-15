package tuya

import (
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

func TestOfferMedias(t *testing.T) {
	audioFirst := func(direction string) []*core.Media {
		return []*core.Media{
			{Kind: core.KindAudio, Direction: direction},
			{Kind: core.KindVideo, Direction: core.DirectionRecvonly},
		}
	}

	tests := []struct {
		name      string
		useOemApi bool
		query     string
		want      []*core.Media
	}{
		{"oem without backchannel", true, "", audioFirst(core.DirectionRecvonly)},
		{"oem backchannel 0", true, "backchannel=0", audioFirst(core.DirectionRecvonly)},
		{"oem backchannel 1", true, "backchannel=1", audioFirst(core.DirectionSendRecv)},
		{"smart or cloud without backchannel", false, "", audioFirst(core.DirectionSendRecv)},
		{"smart or cloud backchannel 0", false, "backchannel=0", audioFirst(core.DirectionSendRecv)},
		{"smart or cloud backchannel 1", false, "backchannel=1", audioFirst(core.DirectionSendRecv)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, err := url.ParseQuery(test.query)
			if err != nil {
				t.Fatal(err)
			}

			if got := offerMedias(test.useOemApi, query); !reflect.DeepEqual(got, test.want) {
				t.Errorf("offerMedias = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestWaitConnectedTimesOut(t *testing.T) {
	client := &Client{}

	err := waitConnected(client, 20*time.Millisecond)
	if err == nil || err.Error() != "connect timeout" {
		t.Fatalf("waitConnected = %v, want connect timeout", err)
	}

	// The waiter is released, so a second wait returns at once.
	if err := client.connected.Wait(); err == nil || err.Error() != "connect timeout" {
		t.Errorf("connected = %v, want connect timeout", err)
	}
}
