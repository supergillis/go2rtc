package tuya

import (
	"maps"
	"net/url"
	"reflect"
	"testing"
)

// Expected values come from the reference implementation run on the same fake inputs.
func fakeOemSession() OemSession {
	return OemSession{
		Sid:             "fake-sid",
		Ecode:           "fake-ecode",
		PartnerIdentity: "fake-partner",
		AppKey:          "fake-app-key",
		SigningKey:      "fake-signing-key",
		ChKey:           "fakech01",
		PackageName:     "com.fake.app",
		ClientDeviceId:  "fake-client-device",
	}
}

func TestOemSign(t *testing.T) {
	session := fakeOemSession()
	params := url.Values{
		"a":          {"smartlife.m.rtc.config.get"},
		"v":          {"1.0"},
		"time":       {"1700000000"},
		"appVersion": {"1.8.0"},
		"clientId":   {"fake-app-key"},
		"deviceId":   {"fake-client-device"},
		"et":         {"0.0.1"},
		"lang":       {"en_US"},
		"os":         {"Android"},
		"requestId":  {"00000000-0000-0000-0000-000000000001"},
		"sid":        {"fake-sid"},
		"chKey":      {"fakech01"},
		"ttid":       {"sdk_international@fake-app-key"},
		"channel":    {"oem"},
		"postData":   {`{"devId":"fake-device"}`},
	}

	tests := []struct {
		name   string
		mutate func(url.Values)
		want   string
	}{
		{"with post data", func(url.Values) {}, "6698a03d12647cbb3b80c0e2ed08dd5b5c38bc92478efbfcdeb2b392aea81632"},
		{"without post data", func(p url.Values) { p.Del("postData") }, "1301a4646702bb866985a6140abee39deae8b7b3bb73b3325b5ede2fd36d3d71"},
		{"params outside the whitelist are ignored", func(p url.Values) {
			p.Del("postData")
			p.Set("sign", "ignored")
			p.Set("platform", "ignored")
		}, "1301a4646702bb866985a6140abee39deae8b7b3bb73b3325b5ede2fd36d3d71"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copied := maps.Clone(params)
			test.mutate(copied)

			if got := session.sign(copied); got != test.want {
				t.Errorf("sign = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOemMqttConfig(t *testing.T) {
	session := fakeOemSession()
	got := session.mqttConfig("fake-uid", "m1.fake.example", "0a1b2c3d")

	want := &MQTTConfig{
		Url:            "ssl://m1.fake.example:8883",
		PublishTopic:   "/av/moto/moto_id/u/{device_id}",
		SubscribeTopic: "/av/u/fake-uid",
		ClientID:       "com.fake.app_mb_fake-client-device_3d90ff5f747f90ded993b62fbf202a76_DEFAULT_0a1b2c3d",
		Username:       "fake-partner_v1_fake-app-key_fakech01_mb_fake-sid7a70b833a2d7458b",
		Password:       "ad5eb4c1dabc69f8",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("mqttConfig = %+v, want %+v", got, want)
	}
}
