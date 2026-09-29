# glkvm

Binary `glkvm` drives a GL.iNet Comet X KVM (a PiKVM kvmd fork) over its HTTP API.
Config lives at `$HOME/.config/glkvm/config.json` unless `GLKVM_CONFIG` names another
path; it must be mode 0600, since it holds a plaintext password, and glkvm warns on
stderr (but does not refuse to run) if it is wider. Credentials never go in the repo
or on the command line: they live only in this file, or are overridden per invocation
with `GLKVM_PASSWORD`.

Config file, one entry per device, `default_device` used when `-d`/`--device` is
omitted:

```json
{
  "devices": {
    "arwen": {
      "url": "https://arwen.example.net",
      "user": "admin",
      "password": "CHANGEME",
      "insecure_tls": true
    }
  },
  "default_device": "arwen"
}
```

`insecure_tls: true` skips certificate verification, needed for arwen's self-signed
cert on the tailnet. Every command also takes `-o`/`--output text|json` (default
text), `--config PATH`, and `--timeout` (default 15s, per request).

Exit codes: 0 ok, 1 device or API failure, 2 usage error, 3 config error (`doctor` is
the one exception: it always exits 1 when any of its required checks fails, never 3,
even when the failure is an unreadable config).

## Build

```
./dev go build -o glkvm ./cmd/glkvm
```

Never build Go on the host; `./dev` runs the pinned container. Verify the result is
static before shipping it anywhere:

```
file glkvm
# glkvm: ELF 64-bit LSB executable, x86-64, ..., statically linked
```

`glkvm` itself is gitignored; never commit the binary.

## devices, doctor

```
glkvm devices
```

Lists configured device names and URLs, marking the default with `*`. Never prints
passwords.

```
glkvm doctor -d arwen -o json
```

Runs config, reach, auth, version, switch, capture, hid and msd checks, in that
order, plus two informational ones (otg, mouse) that report state but never fail the
command. All checks always run, even after an earlier one fails, so one report shows
the full state. Exits 1 if any of the required checks failed.

## status, port

```
glkvm status -d arwen
```

Model, firmware, active port, HDMI/USB link per port, capture resolution, HID state,
flashed EDID profile.

```
# port takes 1..4, matching the switch's own 1.N port IDs; this is NOT the same as
# the device's bare-integer port query parameter, which is a 0-based index
glkvm port -d arwen 4
# next/prev do NOT wrap: next from port 4 and prev from port 1 are no-ops,
# reported as "changed": false (json) or "already at the last/first port"
# (text), exit 0
glkvm port -d arwen next
```

With no argument `port` only reports state; `next`/`prev` advance or retreat one
port. Both wait up to `--settle` (default 10s) for the target port to become active
with a valid capture.

## screenshot

```
glkvm screenshot -d arwen --file shot.jpg --wait-signal 5s
```

JPEG from the streamer; `--png` converts it. `--wait-signal` retries until the
capture is valid instead of failing immediately on no signal. With no `--file` (or
`--file -`), the image bytes go to stdout, but only when stdout is not a terminal
(e.g. piped or redirected); glkvm refuses to dump raw image bytes onto a terminal,
and this also cannot be combined with `-o json`, since stdout would otherwise carry
both the json envelope and the image.

## edid

```
glkvm edid -d arwen
# EDID modes must pass the gsv1127x capture rules or the port shows no signal:
# width and height both divisible by 4 and above 120, at one of the fps values in
# {24,25,29,30,31,49,50,51,59,60,61,75,76,90,91,119,120,121}
glkvm edid validate my.edid.hex
# flashing renegotiates the host's display; the capture takes about 6s to settle
glkvm edid set -d arwen chromebook
```

Profiles: `4k` (3840x2160@30), `2k` (2560x1440@60, factory default), `1k`
(1920x1080@60), `chromebook` (1200x752@60, matches the capture device's native
mode). One EDID covers all four HDMI inputs. `edid set --file PATH` flashes a raw hex
file instead of a named profile; `edid dump` writes the currently flashed hex.
`edid set --force` flashes even when the mode fails the gsv1127x capture rules
above; `--settle` (default 15s) bounds how long it waits, after flashing, for the
capture to land on the new mode. A bare 128-byte (256 hex char) `--file` still
compares and reads back correctly even though the device appends its own CEA
extension, turning it into 512 hex chars once flashed.

## key, type

```
# key and type go to whatever host has focus on the currently selected port; check
# glkvm port first if unsure which host that is
# the device's kvmd lacks F13-F19 and F21-F24, even though glkvm accepts those
# names syntactically
glkvm key -d arwen ctrl+alt+del
glkvm type -d arwen "hello"
```

`key` takes one or more combos (`ctrl+alt+del`, `f5`); `--hold DURATION` presses and
holds instead of tapping. `type --stdin` reads text from stdin instead of an
argument. Both (and every `mouse` subcommand) take `--file PATH` to capture a
screenshot right after the action, and `--observe-delay` (default 300ms, 0 to
disable) to wait for the capture to catch up first. If the action itself succeeded
but that screenshot failed, the error says so ("action completed (N actions), but
the screenshot failed: ...") and still exits 1, so a failed screenshot is never
mistaken for the action itself not having happened.

## mouse

```
glkvm mouse move -d arwen 100 200
glkvm mouse click -d arwen 100 200 --button right
# mouse nudge needs the kvmd mouse output set to usb_rel or usb_hybrid; in usb
# (absolute) mode it refuses instead of silently doing nothing
glkvm mouse nudge -d arwen --dx 5 --dy 0
```

Coordinates are screenshot pixels, scaled to kvmd's absolute range against the
capture's current resolution (or `--size WxH` to skip that lookup). Also `mouse
double-click`, `mouse drag X1 Y1 X2 Y2`, `mouse scroll up|down|left|right [N]`.

## msd

```
glkvm msd -d arwen
glkvm msd upload -d arwen disk.img
# msd attach rebuilds the device's USB gadget: the host's keyboard and mouse drop
# for a few seconds while it does. Re-attaching the same image with the same
# cdrom/rw flags is a no-op (reported as "changed": false) and touches nothing
glkvm msd attach -d arwen disk.img --flash --rw
# detach also rebuilds the gadget, so the host re-enumerates USB the same way
glkvm msd detach -d arwen
glkvm msd remove -d arwen disk.img
```

`attach` defaults to read-only CD-ROM; `--flash` attaches as a flash drive, `--rw`
(requires `--flash`) makes it writable. `upload --replace` overwrites an existing
image of the same name; the check that a replacement fits counts the old image's
own size as space that will be freed, since it is removed only after that check
passes. `detach --keep-usb` disconnects the drive without turning the device's
start_cdrom USB function off, so the host's USB does not re-enumerate.

## run

```
glkvm run -d arwen --actions-json '[{"type":"key","keys":"ctrl+alt+del"},{"type":"wait","ms":500}]'
```

Runs a bounded batch (at most 100 actions) of key/type/move/click/double_click/
scroll/nudge/wait/port/screenshot steps, stopping at the first failure. Every
attempted action gets a receipt with its outcome, even the one that failed.
`--observe-after --file PATH` takes a screenshot after the last action, only if the
whole batch succeeded; if that screenshot itself fails, the error says the batch
completed before naming the screenshot failure, same as `--file` on key/type/mouse.
An unknown field on any action (e.g. a typo'd `"buton"`) is a usage error naming the
action's index, not a silently ignored field.

Action schema, one object per batch entry:

* `{"type":"key","keys":"ctrl+alt+del"}`
* `{"type":"type","text":"hello","slow":false}`
* `{"type":"move","x":100,"y":200}`
* `{"type":"click","x":100,"y":200,"button":"left"}` (`button` optional: left, right or middle)
* `{"type":"double_click","x":100,"y":200}`
* `{"type":"scroll","dx":0,"dy":3}` (positive `dy` scrolls up, positive `dx` scrolls right)
* `{"type":"nudge","dx":5,"dy":0}`
* `{"type":"wait","ms":500}`
* `{"type":"port","port":2}`
* `{"type":"screenshot","file":"step.jpg"}`

```
# a "screenshot" action gets no observe delay of its own, unlike --file on
# key/type/mouse or --observe-after: add an explicit "wait" action before one if
# the capture needs time to catch up with whatever action came before it
glkvm run -d arwen --actions-json '[{"type":"key","keys":"f13"},{"type":"wait","ms":300},{"type":"screenshot","file":"step.jpg"}]'
```

## GLKVM_PASSWORD

```
env GLKVM_PASSWORD=hunter2 glkvm status -d arwen
```

Overrides the configured password for one invocation, without touching the config
file.
