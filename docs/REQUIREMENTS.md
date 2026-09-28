# glkvm: a CLI for arwen

## North star

Drive arwen, the GL.iNet Comet X, from a terminal or a script without opening the web UI.
One Go binary that switches the host port, swaps between three EDID profiles, takes a
screenshot, and types, clicks and moves the mouse on whichever host is selected. Every
command is scriptable: stable exit codes, `--output=json`, no interactive prompts unless
asked for. An agent can use the same binary through MCP later.

## Device facts (Locked)

* arwen is a Comet X, model GL-RM4PE: 4 HDMI inputs, 4 USB-C host ports, 1 HDMI loop-out,
  PoE, Rockchip RV1126B. `/api/upgrade/version` reports model `RM4PE`, version
  `V1.10.1 release3`.
* LAN `192.0.2.10`, tailnet `198.51.100.18`, `https://arwen.example.net`. The API
  answers over HTTPS on the tailnet with a self-signed certificate. ssh stalls at the banner
  over the tailnet and only works on the LAN address.
* Firmware runs PiKVM kvmd 4.82 with GL.iNet additions. Upstream kvmd is cloned in
  `reference/kvmd`; the GL additions exist only as bytecode on the device.
* Auth is per request: `X-KVMD-User` and `X-KVMD-Passwd` headers. `auth.yaml` is empty,
  so the htpasswd backend applies. 2FA (`totp.secret`) exists on the device and must be
  handled if enabled.
* HDMI capture is a gsv1127x. It only captures modes with width and height divisible by 4,
  both above 120, at fps in {24, 25, 29, 30, 31, 49, 50, 51, 59, 60, 61, 75, 76, 90, 91,
  119, 120, 121}. Anything else passes through to the loop-out but captures as no signal.
* EDID presets live in `/etc/kvmd/edid.json` (default `E2560x1440`), the last custom paste
  in `/etc/kvmd/user/edid.txt`, flashed by `gsv1127x_upgrade`. One EDID covers all four
  HDMI inputs; the switch's per-port `edid_id` belongs to the PiKVM Switch and is unused.
* The four host ports are kvmd switch ports `1.1` to `1.4` (channels 0 to 3). GL wires
  them into kvmd's switch module, so upstream's switch API applies. `/etc/kvmd/channel.conf`
  holds the active channel.
* `GET /api/switch` → `summary.active_id` / `summary.active_port` is the active host,
  `video.links[]` is HDMI signal per port, `usb_otg.links[]` is USB attach per port.
  On 2026-09-29 port `1.4` was active, all four HDMI links were up, and USB was attached
  on ports 2 to 4 but not port 1.

## API surface found on the device

Routes extracted from `/usr/lib/python3.12/site-packages/kvmd/apps/kvmd/api/*.pyc`,
served under `/api` by nginx. Calls marked verified were made against arwen on 2026-09-29.

* Screenshot: `GET /streamer/snapshot` returns `image/jpeg` at the capture resolution.
  Verified: a 1200x752 JPEG of pippin's screen, current to the second, matching what the
  web UI renders over WebRTC. Also
  `/streamer/ocr`.
* EDID flash: `POST /upgrade/edid` with form field `edid` holding 256 or 512 hex chars
  (whitespace stripped). 128-byte EDIDs get a stock CEA extension appended. The handler
  writes `/etc/kvmd/user/edid.bin` and `edid.txt`, runs `gsv1127x_upgrade` (model `rm4pe`),
  and returns `{"status": "success"}` or 400 with the tool's stderr. Recovered from the
  bytecode; not yet called.
* EDID read: `GET /upgrade/get_edid` → `result.edid` (hex), verified. `GET /upgrade/edid_list`
  → the presets in `edid.json` with `key`, `label`, `content`, `hz`, `is_default`, verified.
* Port switch: `POST /switch/set_active?port=<n>` takes a port number (upstream
  `valid_float_f0`, so `1.2` or its index form; to confirm which on the device),
  `POST /switch/set_active_next`, `set_active_prev`. Not yet called.
* Keyboard: `/hid/events/send_key`, `/hid/events/send_shortcut`, `/hid/print` (type text),
  `/hid/keymaps`.
* Mouse: `/hid/events/send_mouse_move`, `send_mouse_button`, `send_mouse_wheel`,
  `send_mouse_relative`, `send_touch`.
* HID state: `GET /hid`, `/hid/reset`, `/hid/set_connected`, `/hid/set_params`,
  `/hid/set_jiggler_schedule`.
* Switch extras: `/switch/gui_set_active` (GL), `/switch/set_port_params` (port names),
  `/switch/set_beacon`, `/switch/edids/*` (PiKVM Switch EDIDs, not the gsv1127x).
* Power: `/atx`, `/atx/click`, `/atx/power` (driver `glatx`), and `/switch/atx/*`.
* Other: `/info`, `/log`, `/msd/*`, `/wol/*`, `/gpio/*`, `/fingerbot/*`, `/recorder/*`,
  websocket `/ws`.

## EDID profiles (Locked)

Four named profiles, flashed through `POST /upgrade/edid`. Preset hex comes from
`edid_list` at runtime; the Chromebook hex ships with the tool.

* `4k`: preset `E3840x2160`, 3840x2160@30, Lontium.
* `2k`: preset `E2560x1440`, 2560x1440@60, GLKVM, the factory default.
* `1k`: preset `E1920x1080`, 1920x1080@60, ASUS. The `E1920x1080AOC` preset at 120 Hz is
  the alternative.
* `chromebook`: the GLKVM 2K EDID with its first detailed timing replaced by
  1200x752@59.8 (73.25 MHz pixel clock). It is flashed on arwen now and captures at
  `1200x752@60`, which settles the earlier untested 1200x752 substitute.

```
00ffffffffffff001d891cc28a0e0000081f0103803c22782a2d71af4f44a9270d5054210800d1c095c095008180814081c0010101019d1cb07041f01d2040783a0020542100001c000000ff003839313234370a202020202020000000fc00474c4b564d0a20202020202020000000fd00304b1e721e000a20202020202001e9020327f04b101f051404130312021101230907078301000065030c001000681a00000101304b00023a801871382d40582c450055502100001e8c0ad08a20e02d10103e96005550210000188c0ad090204031200c405500555021000018f03c00d051a0355060883a0055502100001c00000000000000000000000000000000ae
```

`glkvm edid arwen` reports which profile matches the flashed EDID by comparing hex.

## Commands (Open until the design is approved)

Shape follows `reference/jetkvm-cli`: device alias as the first positional argument, one
verb per operation, `--output=json` everywhere, `--file` for images.

* `glkvm status arwen`: model, firmware, active port, signal and resolution, current EDID,
  HID connected, ATX state.
* `glkvm screenshot arwen --file screen.jpg`: JPEG from the streamer, optional `--png`
  conversion, `--wait-signal` to retry until capture is valid.
* `glkvm port arwen` shows the active port with HDMI and USB link state for all four;
  `glkvm port arwen 2`, `next`, `prev` switch it and wait for capture to settle.
* `glkvm edid arwen` shows the flashed EDID and which profile it matches;
  `glkvm edid arwen 4k|2k|1k|chromebook` flashes one; `glkvm edid validate <file>` checks a
  raw EDID against the gsv1127x capture rules before it goes near the device.
* `glkvm key arwen ctrl+alt+del`, `glkvm type arwen "text"`.
* `glkvm mouse arwen move|click|double-click|drag|scroll` with absolute coordinates in
  screenshot pixels, converted to kvmd's absolute range.
* `glkvm run arwen --actions-json '[...]'`: bounded batch of key, type, mouse, wait, port,
  screenshot steps, with `--observe-after`.
* `glkvm msd arwen` shows virtual drive state and stored images; `msd upload <file>`,
  `msd attach <image> [--cdrom|--flash]`, `msd detach`, `msd remove <image>`. Needed to boot
  durin from a NixOS installer for its migration.
* `glkvm power arwen [on|off|reset|off-hard]` over ATX. Later, once an ATX board is attached.
* `glkvm devices` lists configured devices; `glkvm doctor arwen` checks reachability, auth,
  TLS and capabilities.
* `glkvm mcp`: stdio MCP server exposing the same operations. Later, not in the first cut.

## Requirements (Locked)

* Go, one static binary, no cgo. No Python anywhere in the tool.
* Talks to the device only over the kvmd HTTP API. No ssh in the normal path.
* Credentials never go in the repo or on the command line. Read them from a config file
  outside git or from the environment.
* Failures surface: non-2xx or `"ok": false` from kvmd is an error with kvmd's message and
  a non-zero exit. No silent retries of input actions.
* Port switch and EDID flash confirm the result by reading state back, not by trusting the
  POST.

## Live testing rules (Locked, agreed 2026-09-29)

* Port 4 is pippin, the workstation this is developed on. The chromebook profile is just a
  resolution that suits the Chromebook used to view it.
* Ports may be switched freely during testing. Leave arwen on port 4 when done.
* All four EDID profiles may be flashed. Leave `chromebook` flashed when done.
* HID tests on pippin go to an empty text editor javier left focused. Confirm the editor
  still has focus before sending anything; send plain text, Backspace, arrows and F13 to
  F24 only, no shortcuts, no Enter outside the editor, no clicks. Verify through
  screenshots and `/dev/input/by-id/usb-Glinet_Glinet_Composite_Device_CAFEBABE-*`.
* Port 3 is durin at a getty login prompt. Typing there is allowed if it is erased with
  Backspace; never press Enter.
* MSD test: upload a tiny generated image, attach it to port 4, confirm pippin sees the
  USB disk, detach and delete it.
* pippin's GNOME autolock was disabled for unattended work (`idle-delay` was 300,
  `lock-enabled` was true). Restore both when done.

## Open questions

* Whether `set_active` wants `port=1.2` or `port=1`, and how long capture takes to settle
  after a switch. Needs one live switch.
* Whether a port switch also moves USB and ATX to the new host. `usb_otg.links` suggests
  USB follows the port.
* What `/switch/gui_set_active` does differently from `set_active`.
* ATX: `/api/atx` reports `enabled: false`, so no ATX board is attached. Power commands
  stay out of the first cut.
* Tool name: `glkvm` is a placeholder.

## References

References are cloned outside this repo, in `../reference/`.

* `reference/jetkvm-cli`: command shape, batching, receipts, MCP. JetKVM uses WebRTC
  JSON-RPC, so none of its protocol code applies.
* `reference/kvmd`: upstream PiKVM kvmd, the source of truth for the HID, streamer,
  switch and ATX handlers the GL firmware inherits.
