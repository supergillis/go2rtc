# Tuya

[`new in v1.9.13`](https://github.com/AlexxIT/go2rtc/releases/tag/v1.9.13) by [@seydx](https://github.com/seydx)

[Tuya](https://www.tuya.com/) is a proprietary camera protocol with **two-way audio** support. go2rtc supports `Tuya Smart API`, `Tuya Cloud API` and `Tuya OEM app session`.

**Tuya Smart API (recommended)**:
- **Smart Life accounts are NOT supported**, you need to create a Tuya Smart account. If the cameras are already added to the Smart Life app, you need to remove them and add them again to the [Tuya Smart](https://play.google.com/store/apps/details?id=com.tuya.smart) app.
- Cameras can be discovered through the go2rtc web interface via Tuya Smart account (Add > Tuya > Select region and fill in email and password > Login).

**Tuya Cloud API**:
- Requires setting up a cloud project in the Tuya Developer Platform.
- Obtain `device_id`, `client_id`, `client_secret`, and `uid` from [Tuya IoT Platform](https://iot.tuya.com/). [Here's a guide](https://xzetsubou.github.io/hass-localtuya/cloud_api/).
- Please ensure that you have subscribed to the `IoT Video Live Stream` service (Free Trial) in the Tuya Developer Platform, otherwise the stream will not work (Tuya Developer Platform > Service API > Authorize > IoT Video Live Stream).

**Tuya OEM app session**:
- For white-label apps built on the Tuya mobile SDK, such as Philips Avent. These apps do not accept a Tuya Smart login, so go2rtc uses the session of the app instead.
- OEM mode is chosen when `sid` is present. All params below are then required, and a missing one is named in the error.
- `sid`, `ecode`, `partner_identity`, `app_key`, `signing_key`, `ch_key`, `package_name` and `client_device_id` come from the app. `signing_key` is the composed key as the app uses it.
- The host must be the API host of the data center that issued the `sid`.
- Put `sid`, `ecode`, `app_key` and `signing_key` in the `env:` section or the environment and reference them as `${TUYA_SID}`. go2rtc masks these values in logs and in the streams API.
- If the session changes, use an `echo:` source that prints the URL. go2rtc logs the resolved URL of an `echo:` source at debug level.
- Each stream connects to MQTT with the client id of the app plus a random suffix, so several cameras on one account do not drop each other.
- The `uid` and the MQTT broker are read from the account, they are not params. The session is not refreshed, so a new `sid` is needed when it expires.

## Configuration

Use the `resolution` parameter to select the stream (not all cameras support an `hd` stream through WebRTC even if the camera supports it):
- `hd` - HD stream (default)
- `sd` - SD stream

```yaml
streams:
  # Tuya Smart API: WebRTC main stream (use Add > Tuya to discover the URL)
  tuya_main:
    - tuya://protect-us.ismartlife.me?device_id=XXX&email=XXX&password=XXX

  # Tuya Smart API: WebRTC sub stream (use Add > Tuya to discover the URL)
  tuya_sub:
    - tuya://protect-us.ismartlife.me?device_id=XXX&email=XXX&password=XXX&resolution=sd

  # Tuya Cloud API: WebRTC main stream
  tuya_webrtc:
   - tuya://openapi.tuyaus.com?device_id=XXX&uid=XXX&client_id=XXX&client_secret=XXX
  
  # Tuya Cloud API: WebRTC sub stream
  tuya_webrtc_sd:
   - tuya://openapi.tuyaus.com?device_id=XXX&uid=XXX&client_id=XXX&client_secret=XXX&resolution=sd

  # Tuya OEM app session: the values come from the env section or the environment
  tuya_oem:
   - tuya://a1.tuyaeu.com?device_id=XXX&sid=${TUYA_SID}&ecode=${TUYA_ECODE}&partner_identity=XXX&app_key=${TUYA_APP_KEY}&signing_key=${TUYA_SIGNING_KEY}&ch_key=XXX&package_name=XXX&client_device_id=XXX
```
