package tuya

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/webrtc"
	"github.com/google/uuid"
)

const (
	oemAppVersion = "1.8.0"
	oemSdkVersion = "6.7.0"

	// Fixed salt the Tuya mobile SDK mixes into the uid for its MQTT client id.
	oemClientIdSalt = "sdkfasodifca"
)

// OemSession holds what a white-label app stores after login. The signing key
// is the composed key as the app uses it, not its parts.
type OemSession struct {
	Sid             string
	Ecode           string
	PartnerIdentity string
	AppKey          string
	SigningKey      string
	ChKey           string
	PackageName     string
	ClientDeviceId  string
}

// Only these params take part in the request signature.
var oemSignKeys = map[string]bool{
	"a": true, "v": true, "lat": true, "lon": true, "lang": true, "deviceId": true,
	"appVersion": true, "ttid": true, "isH5": true, "h5Token": true, "os": true,
	"clientId": true, "postData": true, "time": true, "requestId": true, "et": true,
	"n4h5": true, "sid": true, "chKey": true, "sp": true,
}

type oemUserInfo struct {
	Id     string `json:"id"`
	Domain struct {
		MobileMqttsUrl string `json:"mobileMqttsUrl"`
	} `json:"domain"`
}

type TuyaOemApiClient struct {
	TuyaClient
	session OemSession
	apiUrl  string
}

func NewTuyaOemApiClient(apiHost, deviceId string, session OemSession) (*TuyaOemApiClient, error) {
	return &TuyaOemApiClient{
		TuyaClient: TuyaClient{
			httpClient: &http.Client{Timeout: 15 * time.Second},
			mqtt:       NewTuyaMqttClient(deviceId),
			deviceId:   deviceId,
			baseUrl:    apiHost,
		},
		session: session,
		apiUrl:  fmt.Sprintf("https://%s/api.json", apiHost),
	}, nil
}

// Init follows the call order of the vendor app: pre-link, config, session init.
// A failed pre-link is only reported when the config call fails too, as the
// server then often refuses the config because of it.
func (c *TuyaOemApiClient) Init() error {
	_, preLinkErr := c.call("smartlife.m.p2p.main.pre.link.get", map[string]string{"devId": c.deviceId})

	webrtcConfig, err := c.loadWebrtcConfig()
	if err != nil {
		if preLinkErr != nil {
			return fmt.Errorf("failed to load webrtc config: %w, after failed pre-link: %v", err, preLinkErr)
		}
		return fmt.Errorf("failed to load webrtc config: %w", err)
	}

	_, _ = c.call("smartlife.m.rtc.session.init", map[string]string{"devId": c.deviceId})

	hubConfig, err := c.loadHubConfig()
	if err != nil {
		return fmt.Errorf("failed to load hub config: %w", err)
	}

	if err := c.mqtt.Start(hubConfig, webrtcConfig, c.skill.WebRTC); err != nil {
		return fmt.Errorf("failed to start MQTT: %w", err)
	}

	if c.skill.LowPower > 0 {
		_ = c.mqtt.WakeUp(c.localKey)
	}

	return nil
}

func (c *TuyaOemApiClient) GetStreamUrl(streamType string) (string, error) {
	return "", errors.New("not supported")
}

func (c *TuyaOemApiClient) loadWebrtcConfig() (*WebRTCConfig, error) {
	raw, err := c.call("smartlife.m.rtc.config.get", map[string]string{"devId": c.deviceId})
	if err != nil {
		return nil, err
	}

	var oemConfig SmartApiWebRTCConfig
	if err = json.Unmarshal(raw, &oemConfig); err != nil {
		return nil, err
	}

	config := oemConfig.toWebRTCConfig()

	// Some cameras report no skill, which would leave a nil skill behind.
	c.skill = &Skill{}
	if config.Skill != "" {
		if err = json.Unmarshal([]byte(config.Skill), c.skill); err != nil {
			return nil, err
		}
	}

	c.localKey = oemConfig.LocalKey

	iceServers, err := json.Marshal(&config.P2PConfig.Ices)
	if err != nil {
		return nil, err
	}

	if c.iceServers, err = webrtc.UnmarshalICEServers(iceServers); err != nil {
		return nil, err
	}

	return config, nil
}

// loadHubConfig takes the uid and the broker from the account, as the session
// itself only proves who is logged in.
func (c *TuyaOemApiClient) loadHubConfig() (*MQTTConfig, error) {
	raw, err := c.call("smartlife.m.user.info.get", nil)
	if err != nil {
		return nil, err
	}

	var info oemUserInfo
	if err = json.Unmarshal(raw, &info); err != nil {
		return nil, err
	}

	if info.Id == "" || info.Domain.MobileMqttsUrl == "" {
		return nil, errors.New("user info has no uid or mqtt url")
	}

	return c.session.mqttConfig(info.Id, info.Domain.MobileMqttsUrl), nil
}

func (s *OemSession) mqttConfig(uid, brokerHost string) *MQTTConfig {
	return &MQTTConfig{
		Url:            fmt.Sprintf("ssl://%s:8883", brokerHost),
		PublishTopic:   "/av/moto/moto_id/u/{device_id}",
		SubscribeTopic: "/av/u/" + uid,
		ClientID:       s.mqttClientId(uid),
		Username:       s.mqttUsername(),
		Password:       s.mqttPassword(),
	}
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *OemSession) mqttPassword() string {
	return md5Hex(md5Hex(s.SigningKey) + s.Ecode)[8:24]
}

func (s *OemSession) mqttUsername() string {
	tail := md5Hex(md5Hex(s.AppKey) + s.Ecode)
	return fmt.Sprintf("%s_v1_%s_%s_mb_%s%s",
		s.PartnerIdentity, s.AppKey, s.ChKey, s.Sid, tail[len(tail)-16:])
}

func (s *OemSession) mqttClientId(uid string) string {
	return fmt.Sprintf("%s_mb_%s_%s_DEFAULT",
		s.PackageName, s.ClientDeviceId, md5Hex(uid+oemClientIdSalt))
}

// swapMd5 reorders the four 8 char blocks of a postData digest the way the SDK does.
func swapMd5(digest string) string {
	if len(digest) != 32 {
		return digest
	}
	return digest[8:16] + digest[0:8] + digest[24:32] + digest[16:24]
}

func (s *OemSession) sign(params map[string]string) string {
	pairs := make([]string, 0, len(params))
	for key, value := range params {
		if !oemSignKeys[key] || value == "" {
			continue
		}
		if key == "postData" {
			value = swapMd5(md5Hex(value))
		}
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)

	mac := hmac.New(sha256.New, []byte(s.SigningKey))
	mac.Write([]byte(strings.Join(pairs, "||")))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *TuyaOemApiClient) buildParams(action string, postData any) (map[string]string, error) {
	session := &c.session
	params := map[string]string{
		"a":                 action,
		"v":                 "1.0",
		"time":              fmt.Sprintf("%d", time.Now().Unix()),
		"appVersion":        oemAppVersion,
		"appRnVersion":      "5.92",
		"channel":           "oem",
		"chKey":             session.ChKey,
		"clientId":          session.AppKey,
		"cp":                "gzip",
		"deviceCoreVersion": oemSdkVersion,
		"deviceId":          session.ClientDeviceId,
		"et":                "0.0.1",
		"nd":                "1",
		"lang":              "en_US",
		"os":                "Android",
		"osSystem":          "14",
		"platform":          "tuya_bridge",
		"requestId":         uuid.New().String(),
		"sdkVersion":        oemSdkVersion,
		"sid":               session.Sid,
		"timeZoneId":        "Europe/Rome",
		"ttid":              "sdk_international@" + session.AppKey,
	}

	if postData != nil {
		body, err := json.Marshal(postData)
		if err != nil {
			return nil, err
		}
		params["postData"] = string(body)
	}

	params["sign"] = session.sign(params)
	return params, nil
}

func (c *TuyaOemApiClient) call(action string, postData any) (json.RawMessage, error) {
	params, err := c.buildParams(action, postData)
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}

	req, err := http.NewRequest("POST", c.apiUrl, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", fmt.Sprintf("Thing-UA=APP/Android/%s/SDK/%s", oemAppVersion, oemSdkVersion))

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var response struct {
		Result    json.RawMessage `json:"result"`
		Success   bool            `json:"success"`
		ErrorCode string          `json:"errorCode"`
		ErrorMsg  string          `json:"errorMsg"`
	}
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("%s: invalid response: %w", action, err)
	}

	if !response.Success {
		return nil, fmt.Errorf("%s: %s (code: %s)", action, response.ErrorMsg, response.ErrorCode)
	}

	return response.Result, nil
}

// oemSessionFromQuery reads the OEM params and names every missing one.
func oemSessionFromQuery(query url.Values) (OemSession, error) {
	session := OemSession{
		Sid:             query.Get("sid"),
		Ecode:           query.Get("ecode"),
		PartnerIdentity: query.Get("partner_identity"),
		AppKey:          query.Get("app_key"),
		SigningKey:      query.Get("signing_key"),
		ChKey:           query.Get("ch_key"),
		PackageName:     query.Get("package_name"),
		ClientDeviceId:  query.Get("client_device_id"),
	}

	var missing []string
	for _, name := range []string{
		"signing_key", "app_key", "ecode", "partner_identity", "ch_key", "package_name", "client_device_id",
	} {
		if query.Get(name) == "" {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		return session, fmt.Errorf("missing oem params: %s", strings.Join(missing, ", "))
	}

	return session, nil
}
