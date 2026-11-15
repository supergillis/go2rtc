package tuya

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/creds"
	"github.com/AlexxIT/go2rtc/pkg/webrtc"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

type Client struct {
	api       TuyaAPI
	conn      *webrtc.Conn
	pc        *pion.PeerConnection
	connected core.Waiter
	closed    bool

	// Only OEM dials are judged, see judgeFailure.
	oem      bool
	deviceId string
	judged   sync.Once
	// asked is set once the offer is sent, so only later errors count against the camera.
	asked atomic.Bool

	// HEVC only:
	dc         *pion.DataChannel
	videoSSRC  *uint32
	audioSSRC  *uint32
	streamType int
	isHEVC     bool
	handlersMu sync.RWMutex
	handlers   map[uint32]func(*rtp.Packet)
}

type DataChannelMessage struct {
	Type string `json:"type"` // "codec", "start", "recv", "complete"
	Msg  string `json:"msg"`
}

// RecvMessage contains SSRC values for video/audio streams
type RecvMessage struct {
	Video struct {
		SSRC uint32 `json:"ssrc"`
	} `json:"video"`
	Audio struct {
		SSRC uint32 `json:"ssrc"`
	} `json:"audio"`
}

// connectTimeout bounds the wait for a camera that ignores the offer.
const connectTimeout = 30 * time.Second

var errConnectTimeout = errors.New("connect timeout")

func Dial(rawURL string) (core.Producer, error) {
	escapedURL := strings.ReplaceAll(rawURL, "#", "%23")
	u, err := url.Parse(escapedURL)
	if err != nil {
		return nil, err
	}

	query := u.Query()

	// Tuya Smart API
	email := query.Get("email")
	password := query.Get("password")

	// Tuya Cloud API
	uid := query.Get("uid")
	clientId := query.Get("client_id")
	clientSecret := query.Get("client_secret")

	// Tuya OEM mobile app session
	sid := query.Get("sid")

	// Shared params
	deviceId := query.Get("device_id")

	// Stream params
	streamResolution := query.Get("resolution")

	useSmartApi := deviceId != "" && email != "" && password != ""
	useCloudApi := deviceId != "" && uid != "" && clientId != "" && clientSecret != ""

	if streamResolution == "" || (streamResolution != "hd" && streamResolution != "sd") {
		streamResolution = "hd"
	}

	// A sid selects the OEM API, so a half filled session fails loudly instead
	// of falling through to the generic wrong params error.
	useOemApi := deviceId != "" && sid != ""

	// Mask before validation, so error messages and the streams API never show them.
	for _, name := range []string{"sid", "ecode", "signing_key", "app_key"} {
		creds.AddSecret(query.Get(name))
	}

	if !useSmartApi && !useCloudApi && !useOemApi {
		return nil, errors.New("tuya: wrong query params")
	}

	client := &Client{
		handlers: make(map[uint32]func(*rtp.Packet)),
		oem:      useOemApi,
	}

	// The slot of an OEM camera stays reserved until the session is judged. An
	// off camera or a panic ends the dial without a verdict and frees it here.
	var slotHeld bool
	defer func() {
		if slotHeld {
			client.judgeStop()
		}
	}()

	if useOemApi {
		oemClient, err := NewTuyaOemApiClient(u.Host, deviceId, query)
		if err != nil {
			return nil, fmt.Errorf("tuya: %w", err)
		}

		if err = oemDialBackoff.acquire(deviceId); err != nil {
			if !errors.Is(err, errInFlight) {
				err = fmt.Errorf("backing off after failed dial: %w", err)
			}
			return nil, fmt.Errorf("tuya: %w", err)
		}
		client.deviceId = deviceId
		slotHeld = true

		if err = checkCameraPower(oemClient); err != nil {
			return nil, fmt.Errorf("tuya: %w", err)
		}
		client.api = oemClient
	} else if useSmartApi {
		if client.api, err = NewTuyaSmartApiClient(nil, u.Hostname(), email, password, deviceId); err != nil {
			return nil, fmt.Errorf("tuya: %w", err)
		}
	} else {
		if client.api, err = NewTuyaCloudApiClient(u.Hostname(), uid, deviceId, clientId, clientSecret); err != nil {
			return nil, fmt.Errorf("tuya: %w", err)
		}
	}

	if err := client.api.Init(); err != nil {
		client.Close(err)
		return nil, fmt.Errorf("tuya: %w", err)
	}

	client.streamType = client.api.GetStreamType(streamResolution)
	client.isHEVC = client.api.IsHEVC(client.streamType)

	// Create a new PeerConnection
	conf := pion.Configuration{
		ICEServers:         client.api.GetICEServers(),
		ICETransportPolicy: pion.ICETransportPolicyAll,
		BundlePolicy:       pion.BundlePolicyMaxBundle,
	}

	api, err := webrtc.NewAPI()
	if err != nil {
		client.Close(err)
		return nil, err
	}

	client.pc, err = api.NewPeerConnection(conf)
	if err != nil {
		client.Close(err)
		return nil, err
	}

	// protect from sending ICE candidate before Offer
	var sendOffer core.Waiter

	// protect from blocking on errors
	defer sendOffer.Done(nil)

	// Create new WebRTC connection
	client.conn = webrtc.NewConn(client.pc)
	client.conn.FormatName = "tuya/webrtc"
	client.conn.Mode = core.ModeActiveProducer
	client.conn.Protocol = "mqtt"

	mqttClient := client.api.GetMqtt()
	if mqttClient == nil {
		err = errors.New("tuya: no mqtt client")
		client.Close(err)
		return nil, err
	}

	// Set up MQTT handlers
	mqttClient.handleAnswer = func(answer AnswerFrame) {
		// fmt.Printf("tuya: answer: %s\n", answer.Sdp)

		desc := pion.SessionDescription{
			Type: pion.SDPTypePranswer,
			SDP:  answer.Sdp,
		}

		if err := client.pc.SetRemoteDescription(desc); err != nil {
			client.Close(err)
			return
		}

		if err := client.conn.SetAnswer(answer.Sdp); err != nil {
			client.Close(err)
			return
		}

		if client.isHEVC {
			// Tuya responds with H264/90000 even for HEVC streams
			// So we need to replace video codecs with HEVC ones from API
			for _, media := range client.conn.Medias {
				if media.Kind == core.KindVideo {
					codecs := client.api.GetVideoCodecs()
					if codecs != nil {
						media.Codecs = codecs
					}
				}
			}

			// Audio codecs from API as well
			// Tuya responds with multiple audio codecs (PCMU, PCMA)
			// But the quality is bad if we use PCMU and skill only has PCMA
			for _, media := range client.conn.Medias {
				if media.Kind == core.KindAudio {
					codecs := client.api.GetAudioCodecs()
					if codecs != nil {
						media.Codecs = codecs
					}
				}
			}
		}
	}

	mqttClient.handleCandidate = func(candidate CandidateFrame) {
		// fmt.Printf("tuya: candidate: %s\n", candidate.Candidate)

		if candidate.Candidate != "" {
			_ = client.conn.AddCandidate(candidate.Candidate)
		}
	}

	mqttClient.handleDisconnect = func() {
		// fmt.Println("tuya: disconnect")
		client.Close(errors.New("mqtt: disconnect"))
	}

	mqttClient.handleError = func(err error) {
		// fmt.Printf("tuya: error: %s\n", err.Error())
		client.Close(err)
	}

	if client.isHEVC {
		maxRetransmits := uint16(5)
		ordered := true
		client.dc, err = client.pc.CreateDataChannel("fmp4Stream", &pion.DataChannelInit{
			MaxRetransmits: &maxRetransmits,
			Ordered:        &ordered,
		})

		// DataChannel receives two types of messages:
		// 1. String messages: Control messages (codec, recv)
		// 2. Binary messages: RTP packets with video/audio
		client.dc.OnMessage(func(msg pion.DataChannelMessage) {
			if msg.IsString {
				// Handle control messages (codec, recv, etc.)
				if connected, err := client.probe(msg); err != nil {
					client.Close(err)
				} else if connected {
					client.connected.Done(nil)
				}
			} else {
				// Handle RTP packets - Route by SSRC retrieved from "recv" message
				packet := &rtp.Packet{}
				if err := packet.Unmarshal(msg.Data); err != nil {
					// Skip invalid packets
					return
				}

				if handler, ok := client.getHandler(packet.SSRC); ok {
					handler(packet)
				}
			}
		})

		client.dc.OnError(func(err error) {
			// fmt.Printf("tuya: datachannel error: %s\n", err.Error())
			client.Close(err)
		})

		client.dc.OnClose(func() {
			// fmt.Println("tuya: datachannel closed")
			client.Close(errors.New("datachannel: closed"))
		})

		client.dc.OnOpen(func() {
			// fmt.Println("tuya: datachannel opened")

			codecRequest, _ := json.Marshal(DataChannelMessage{
				Type: "codec",
				Msg:  "",
			})

			if err := client.sendMessageToDataChannel(codecRequest); err != nil {
				client.Close(fmt.Errorf("failed to send codec request: %w", err))
			}
		})
	}

	// Set up pc handler
	client.conn.Listen(func(msg any) {
		switch msg := msg.(type) {
		case *pion.ICECandidate:
			_ = sendOffer.Wait()
			if err := mqttClient.SendCandidate("a=" + msg.ToJSON().Candidate); err != nil {
				client.Close(err)
			}

		case pion.PeerConnectionState:
			switch msg {
			case pion.PeerConnectionStateNew:
				break
			case pion.PeerConnectionStateConnecting:
				break
			case pion.PeerConnectionStateConnected:
				// On HEVC, wait for DataChannel to be opened and camera to send codec info
				if !client.isHEVC {
					if streamResolution == "hd" {
						_ = mqttClient.SendResolution(0)
					}
					client.connected.Done(nil)
				}
			case pion.PeerConnectionStateClosed:
				client.Close(errors.New("webrtc: " + msg.String()))
			default:
				// client.Close(errors.New("webrtc: " + msg.String()))
			}
		}
	})

	// Create offer
	offer, err := client.conn.CreateOffer(offerMedias(useOemApi, query))
	if err != nil {
		client.Close(err)
		return nil, err
	}

	// horter sdp, remove a=extmap... line, device ONLY allow 8KB json payload
	// https://github.com/tuya/webrtc-demo-go/blob/04575054f18ccccb6bc9d82939dd46d449544e20/static/js/main.js#L224
	re := regexp.MustCompile(`\r\na=extmap[^\r\n]*`)
	offer = re.ReplaceAllString(offer, "")

	// Send offer
	client.asked.Store(true)
	if err := mqttClient.SendOffer(offer, streamResolution, client.streamType, client.isHEVC); err != nil {
		err = fmt.Errorf("tuya: %w", err)
		client.Close(err)
		return nil, err
	}

	sendOffer.Done(nil)

	// Wait for connection
	if err = waitConnected(client); err != nil {
		err = fmt.Errorf("tuya: %w", err)
		client.Close(err)
		return nil, err
	}

	if useOemApi {
		slotHeld = false
		time.AfterFunc(stableSession, client.judgeStable)
	}

	return client, nil
}

// offerMedias keeps audio first, otherwise tuya will send corrupt sdp.
// A sendrecv audio offer puts OEM cameras in talking mode, so OEM mode is
// receive only unless backchannel=1.
func offerMedias(useOemApi bool, query url.Values) []*core.Media {
	audioDirection := core.DirectionSendRecv
	if useOemApi && query.Get("backchannel") != "1" {
		audioDirection = core.DirectionRecvonly
	}

	return []*core.Media{
		{Kind: core.KindAudio, Direction: audioDirection},
		{Kind: core.KindVideo, Direction: core.DirectionRecvonly},
	}
}

func (c *Client) GetMedias() []*core.Media {
	return c.conn.GetMedias()
}

func (c *Client) GetTrack(media *core.Media, codec *core.Codec) (*core.Receiver, error) {
	return c.conn.GetTrack(media, codec)
}

func (c *Client) AddTrack(media *core.Media, codec *core.Codec, track *core.Receiver) error {
	localTrack := c.conn.GetSenderTrack(media.ID)
	if localTrack == nil {
		return errors.New("webrtc: can't get track")
	}

	// DISABLED: Speaker Protocol 312 command
	// JavaScript client doesn't send this on first call either
	// Only subsequent calls (when speakerChloron is set) send Protocol 312
	// mqttClient := c.api.GetMqtt()
	// if mqttClient != nil {
	// 	_ = mqttClient.SendSpeaker(1)
	// }

	payloadType := codec.PayloadType

	sender := core.NewSender(media, codec)

	switch track.Codec.Name {
	case core.CodecPCMA, core.CodecPCMU, core.CodecPCM, core.CodecPCML:
		// Frame size affects audio delay with Tuya cameras:
		// Browser sends standard 20ms frames (160 bytes for G.711), but this causes
		// up to 4s delay on some Tuya cameras. Increasing to 240 bytes (30ms) reduces
		// delay to ~2s. Higher values (320+ bytes) don't work and cause issues.
		// Using 240 bytes (30ms) as optimal balance between latency and stability.
		frameSize := 240

		var buf []byte
		var seq uint16
		var ts uint32

		sender.Handler = func(packet *rtp.Packet) {
			buf = append(buf, packet.Payload...)

			for len(buf) >= frameSize {
				payload := buf[:frameSize]

				pkt := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						Marker:         true,
						PayloadType:    payloadType,
						SequenceNumber: seq,
						Timestamp:      ts,
						SSRC:           packet.SSRC,
					},
					Payload: payload,
				}

				seq++
				ts += uint32(frameSize)
				buf = buf[frameSize:]

				c.conn.Send += pkt.MarshalSize()
				_ = localTrack.WriteRTP(payloadType, pkt)
			}
		}

	default:
		sender.Handler = func(packet *rtp.Packet) {
			c.conn.Send += packet.MarshalSize()
			_ = localTrack.WriteRTP(payloadType, packet)
		}
	}

	sender.HandleRTP(track)
	c.conn.Senders = append(c.conn.Senders, sender)

	return nil
}

func (c *Client) Start() error {
	if len(c.conn.Receivers) == 0 {
		return errors.New("tuya: no receivers")
	}

	var video, audio *core.Receiver
	for _, receiver := range c.conn.Receivers {
		if receiver.Codec.IsVideo() {
			video = receiver
		} else if receiver.Codec.IsAudio() {
			audio = receiver
		}
	}

	if c.videoSSRC != nil {
		c.setHandler(*c.videoSSRC, func(packet *rtp.Packet) {
			if video != nil {
				video.WriteRTP(packet)
			}
		})
	}

	if c.audioSSRC != nil {
		c.setHandler(*c.audioSSRC, func(packet *rtp.Packet) {
			if audio != nil {
				audio.WriteRTP(packet)
			}
		})
	}

	return c.conn.Start()
}

// judgeFailure counts an error against the camera, once per dial. Only an error
// after the camera was asked for a session counts, as a network error before
// says nothing about the camera.
func (c *Client) judgeFailure(err error) {
	if !c.oem {
		return
	}

	c.judged.Do(func() {
		if c.asked.Load() {
			oemDialBackoff.release(c.deviceId, err)
		} else {
			oemDialBackoff.release(c.deviceId, nil)
		}
	})
}

// judgeStop frees the slot without a verdict, as when a consumer leaves.
func (c *Client) judgeStop() {
	if c.oem {
		c.judged.Do(func() { oemDialBackoff.release(c.deviceId, nil) })
	}
}

// judgeStable clears the failures after the session stayed up long enough.
func (c *Client) judgeStable() {
	if c.oem {
		c.judged.Do(func() { oemDialBackoff.reset(c.deviceId) })
	}
}

func (c *Client) Stop() error {
	c.judgeStop()

	if c.closed {
		return nil
	}

	c.closed = true

	c.clearHandlers()

	if c.conn != nil {
		_ = c.conn.Stop()
	}

	if c.api != nil {
		c.api.Close()
	}

	return nil
}

func (c *Client) Close(err error) error {
	if err != nil {
		c.judgeFailure(err)
	}

	c.connected.Done(err)
	return c.Stop()
}

func (c *Client) MarshalJSON() ([]byte, error) {
	return c.conn.MarshalJSON()
}

func (c *Client) setHandler(ssrc uint32, handler func(*rtp.Packet)) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.handlers[ssrc] = handler
}

func (c *Client) getHandler(ssrc uint32) (func(*rtp.Packet), bool) {
	c.handlersMu.RLock()
	defer c.handlersMu.RUnlock()
	handler, ok := c.handlers[ssrc]
	return handler, ok
}

func (c *Client) clearHandlers() {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	for ssrc := range c.handlers {
		delete(c.handlers, ssrc)
	}
}

func (c *Client) probe(msg pion.DataChannelMessage) (bool, error) {
	// fmt.Printf("[tuya] Received string message: %s\n", string(msg.Data))

	var message DataChannelMessage
	if err := json.Unmarshal([]byte(msg.Data), &message); err != nil {
		return false, err
	}

	switch message.Type {
	case "codec":
		// Camera responded to our codec request - now request frame start
		frameRequest, _ := json.Marshal(DataChannelMessage{
			Type: "start",
			Msg:  "frame",
		})

		err := c.sendMessageToDataChannel(frameRequest)
		if err != nil {
			return false, err
		}

	case "recv":
		// Camera sends SSRC values for video/audio streams
		// We need these to route incoming RTP packets correctly
		var recvMessage RecvMessage
		if err := json.Unmarshal([]byte(message.Msg), &recvMessage); err != nil {
			return false, err
		}

		videoSSRC := recvMessage.Video.SSRC
		audioSSRC := recvMessage.Audio.SSRC
		c.videoSSRC = &videoSSRC
		c.audioSSRC = &audioSSRC

		// Send "complete" to tell camera we're ready to receive RTP packets
		completeMsg, _ := json.Marshal(DataChannelMessage{
			Type: "complete",
			Msg:  "",
		})

		err := c.sendMessageToDataChannel(completeMsg)
		if err != nil {
			return false, err
		}

		return true, nil
	}

	return false, nil
}

func (c *Client) sendMessageToDataChannel(message []byte) error {
	if c.dc != nil {
		// fmt.Printf("[tuya] sending message to data channel: %s\n", message)
		return c.dc.Send(message)
	}

	return nil
}

// waitConnected closes the client from this goroutine when the camera takes too
// long, so a silent camera fails the dial instead of holding it forever.
func waitConnected(client *Client) error {
	done := make(chan error, 1)
	go func() { done <- client.connected.Wait() }()

	timer := time.NewTimer(connectTimeout)
	defer timer.Stop()

	select {
	case err := <-done:
		return err
	case <-timer.C:
		// A connection that finished at the same moment wins over the timeout.
		select {
		case err := <-done:
			return err
		default:
		}

		_ = client.Close(errConnectTimeout)
		return <-done
	}
}
