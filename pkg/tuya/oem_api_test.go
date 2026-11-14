package tuya

import (
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
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
	params := map[string]string{
		"a":          "smartlife.m.rtc.config.get",
		"v":          "1.0",
		"time":       "1700000000",
		"appVersion": "1.8.0",
		"clientId":   "fake-app-key",
		"deviceId":   "fake-client-device",
		"et":         "0.0.1",
		"lang":       "en_US",
		"os":         "Android",
		"requestId":  "00000000-0000-0000-0000-000000000001",
		"sid":        "fake-sid",
		"chKey":      "fakech01",
		"ttid":       "sdk_international@fake-app-key",
		"channel":    "oem",
		"postData":   `{"devId":"fake-device"}`,
	}

	tests := []struct {
		name   string
		mutate func(map[string]string)
		want   string
	}{
		{"with post data", func(map[string]string) {}, "6698a03d12647cbb3b80c0e2ed08dd5b5c38bc92478efbfcdeb2b392aea81632"},
		{"without post data", func(p map[string]string) { delete(p, "postData") }, "1301a4646702bb866985a6140abee39deae8b7b3bb73b3325b5ede2fd36d3d71"},
		{"params outside the whitelist are ignored", func(p map[string]string) {
			delete(p, "postData")
			p["sign"] = "ignored"
			p["platform"] = "ignored"
		}, "1301a4646702bb866985a6140abee39deae8b7b3bb73b3325b5ede2fd36d3d71"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copied := make(map[string]string, len(params))
			for key, value := range params {
				copied[key] = value
			}
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

func TestOemSessionFromQuery(t *testing.T) {
	complete := "sid=fake-sid&ecode=fake-ecode&partner_identity=fake-partner&app_key=fake-app-key" +
		"&signing_key=fake-signing-key&ch_key=fakech01&package_name=com.fake.app&client_device_id=fake-client-device"

	tests := []struct {
		name    string
		query   string
		want    OemSession
		wantErr string
	}{
		{"complete", complete, fakeOemSession(), ""},
		{"only sid", "sid=fake-sid", OemSession{Sid: "fake-sid"},
			"missing oem params: signing_key, app_key, ecode, partner_identity, ch_key, package_name, client_device_id"},
		{"no signing key", "sid=fake-sid&ecode=fake-ecode&partner_identity=fake-partner&app_key=fake-app-key" +
			"&ch_key=fakech01&package_name=com.fake.app&client_device_id=fake-client-device",
			OemSession{
				Sid: "fake-sid", Ecode: "fake-ecode", PartnerIdentity: "fake-partner", AppKey: "fake-app-key",
				ChKey: "fakech01", PackageName: "com.fake.app", ClientDeviceId: "fake-client-device",
			}, "missing oem params: signing_key"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, err := url.ParseQuery(test.query)
			if err != nil {
				t.Fatal(err)
			}

			got, err := oemSessionFromQuery(query)
			if test.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.wantErr != "" && (err == nil || err.Error() != test.wantErr) {
				t.Errorf("error = %v, want %q", err, test.wantErr)
			}
			if got != test.want {
				t.Errorf("session = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestOemWebRTCConfigMapping(t *testing.T) {
	recorded := `{
		"auth": "fake-auth",
		"id": "fake-device",
		"localKey": "fake-local-key",
		"motoId": "fake-moto",
		"p2pConfig": {"ices": [
			{"urls": "stun:stun.fake.example:3478"},
			{"urls": "turn:turn.fake.example:3478", "username": "fake-user", "credential": "fake-pass", "ttl": 36000}
		]},
		"protocolVersion": "2.2",
		"skill": "{\"webrtc\":3}",
		"supportWebrtcRecord": true,
		"supportsWebrtc": true,
		"vedioClarity": 2,
		"videoClarity": 4,
		"vedioClaritys": [2, 4]
	}`

	var config SmartApiWebRTCConfig
	if err := json.Unmarshal([]byte(recorded), &config); err != nil {
		t.Fatal(err)
	}

	want := &WebRTCConfig{
		Auth:   "fake-auth",
		ID:     "fake-device",
		MotoID: "fake-moto",
		P2PConfig: P2PConfig{Ices: []ICEServer{
			{Urls: "stun:stun.fake.example:3478"},
			{Urls: "turn:turn.fake.example:3478", Username: "fake-user", Credential: "fake-pass", TTL: 36000},
		}},
		ProtocolVersion:      "2.2",
		Skill:                `{"webrtc":3}`,
		SupportsWebRTCRecord: true,
		SupportsWebRTC:       true,
		VedioClaritiy:        2,
		VideoClaritiy:        4,
		VideoClarities:       []int{2, 4},
	}

	if got := config.toWebRTCConfig(); !reflect.DeepEqual(got, want) {
		t.Errorf("toWebRTCConfig = %+v, want %+v", got, want)
	}
}

func TestOemClientIdSuffix(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}$`)

	first, err := NewTuyaOemApiClient("a1.fake.example", "fake-device", fakeOemSession())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewTuyaOemApiClient("a1.fake.example", "fake-device", fakeOemSession())
	if err != nil {
		t.Fatal(err)
	}

	for _, suffix := range []string{first.clientIdSuffix, second.clientIdSuffix} {
		if !pattern.MatchString(suffix) {
			t.Errorf("suffix = %q, want 8 lowercase hex chars", suffix)
		}
	}
	if first.clientIdSuffix == second.clientIdSuffix {
		t.Errorf("two clients share the suffix %q", first.clientIdSuffix)
	}
}
