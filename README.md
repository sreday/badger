# Badger

Conference badge generator and printer. Integrates with the Lu.ma event API to fetch guest lists, generates badge images with name/title/company text and QR codes (LinkedIn + WiFi), and prints to Brother QL label printers via CUPS.

Single static binary — no runtime dependencies.

## Badger 2.0, no install: scan.sreday.com/badger

Plug the label printer (and the ticket scanner) into any laptop, open **https://scan.sreday.com/badger/**, sign in with the Snout admin password, pick the event: done. The events and guest lists come from Snout's Luma key, so there is no Luma login. The browser prints each badge on the laptop's printer through its print window. The first time, pick the label printer, paper 62 x 100 mm and no margins; the browser remembers. For a self-service scanner stand, skip the print window: make the label printer the laptop's default printer, quit Chrome, and start it with `--kiosk-printing` (the exact Terminal line is on the page, under the printer card). The jobs list shows this page's prints. The printer's own state (on, off, queue) needs the installed version below, which reads it from CUPS. The hosted version has no CSV import.

The same app is in `badger2/app`. `badger2` runs it on the laptop, and Snout serves it at `/badger` behind its admin password.

## Badger 2.0 (beta): one-line install

Mac or Linux, in Terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/sreday/badger/main/install.sh | sh
```

Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/sreday/badger/main/install.ps1 | iex
```

It removes the old Badger (`badger-darwin-*` / `badger-linux-*` / `badger-windows-*` downloads, to the Trash or Recycle Bin), keeps its `config.json` printer settings in `~/.badger/`, installs `badger2` and opens it at http://localhost:8090. Next time just type `badger2`; run the install line again to update. Printed badges are saved in `~/.badger/badges/`.

Badger 2.0 lives in [`badger2/`](badger2/) (the app itself in [`badger2/app`](badger2/app/)) and prints the new badge: plain purple, Atkinson Hyperlegible, a big LinkedIn QR with the LinkedIn mark, rounded corners, no WiFi QR. The original Badger below keeps working as it is.

**Runs wherever the original Badger runs.** Both are built together, from the same Go toolchain (`go.mod`), for the same machines: macOS 11+ (Intel and Apple Silicon), Windows and Linux, 64-bit. The pages need only a browser from 2017 on (any Chrome, Safari 11+); in an older one the background sweep and glass edges drop out and everything still works. The scanner and printers are plain USB: no drivers.

Badger 2.0 prints to the printers you switch on in its printer panel (one or more, from the printers your Mac or Linux machine knows); the choice is saved as `"printers"` in `~/.badger/config.json`, and the original Badger's two-printer list works as it is. Each badge goes to the switched-on printer with the shortest queue that isn't off (unplugged, paused, offline); with every printer off it waits in the first one's queue. The panel shows each printer's state (printing, idle or off, read from CUPS with `lpstat`) and the jobs: failed, queued and the last few printed, with whose badge each one is and which printer it went to. Pressing print adds the new job there straight away. (The original Badger sends each badge to a random printer from its list.)

**Ticket scanner:** plug a USB or Bluetooth 2D barcode scanner (one that reads phone screens, set to keyboard mode) into the laptop, e.g. into the second printer's port. Scanning an attendee's Luma ticket QR prints their badge straight away: **Directly print badges scanned by the device** is on by default and stays on while a printer is on. With every printer off it pauses (a scan finds the person, prints nothing, and says why) and comes back by itself with the printer. Switched off by hand, a scan only finds the person and Enter prints. A big banner and a beep say what happened: printing, already printed (print again?), another event's ticket, or not on the list. Luma's public API has no check-in call, so scanning doesn't check guests in on Luma.

*Our scanner: Tera Pro 9100* (desk-standing, USB-powered, reads phone screens). Out of the box it types as a US keyboard and presses Enter after each code, which is all Badger needs:

1. Plug it into the laptop (USB-A; on a USB-C-only Mac use an adapter). If macOS opens **Keyboard Setup Assistant**, close it: the scanner is a keyboard that can't answer it.
2. Open the event in Badger 2.0 with a printer on: scanned tickets print straight away.
3. A two-note chime on the laptop says "scanned, take your phone away" (a low buzz for a problem; switch off **Play sound when the badge is scanned by the device**). Each ticket has a 3-second cooldown that restarts with every reread, so a phone left on the scanner (it rereads every half second) counts once; the next person scans straight away. Badger never prints a second badge for someone whose badge is on its way or already printed (**Print again** in the banner if you really want one).
4. On a laptop with a German or French keyboard layout the scans come out garbled; Badger says so. Fix: set the laptop to British/U.S., or scan the scanner's own "Keyboard Country Layout" code for that country (manual TR-UM9100, page 5-7).

## Quick start (Mac M1+)

```bash
VERSION=v0.0.1rc6
curl -LO https://github.com/sreday/badger/releases/download/${VERSION}/badger-darwin-arm64
chmod +x badger-darwin-arm64
./badger-darwin-arm64
```

## Quick start (Mac Intel)

```bash
VERSION=v0.0.1rc6
curl -LO https://github.com/sreday/badger/releases/download/${VERSION}/badger-darwin-amd64
chmod +x badger-darwin-amd64
./badger-darwin-amd64
```

## Quick start

Download a binary from [Releases](../../releases), or build from source:

```
make build
```

Run:

```
./badger
```

Open `http://localhost:8080` in a browser and enter your Lu.ma API key.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HOST` | `0.0.0.0` | Listen address |
| `PORT` | `8080` | Listen port |
| `FONT_PATH` | (embedded Go Mono) | Path to a `.ttf` font file |
| `WIFI_ID` | (none) | WiFi SSID for QR code on badge |
| `WIFI_PASSWORD` | (none) | WiFi password |
| `WIFI_AUTH` | `WPA` | WiFi auth type (`WPA`, `WEP`, `nopass`) |
| `SESSION_KEY` | `badger` | Cookie signing key |
| `DEBUG` | (none) | Set to any value to enable debug logging |
| `BADGE_STYLE` | (new design) | `classic` prints the original badge (Go Mono font, WiFi QR). The WiFi settings only apply to the classic badge. |

## config.json

Place a `config.json` in the working directory:

```json
{
    "print_cmd": "/usr/bin/lpr -P {printer} -o orientation-requested=5 -o PageSize=62x100mm -o Quality=High {path}",
    "printers": [
        "Brother_QL_820NWB",
        "Brother_QL_820NWB_2"
    ]
}
```

## Printing

Requires Brother QL label printers configured via CUPS. The app picks a random printer from the `printers` list in `config.json` for each print job.

## Snout by Badger

Snout, the sponsor lead scanner, is a separate app in [`scanner/`](scanner/README.md): sponsors scan the LinkedIn QR on these badges and get a CSV of their leads matched to the Luma guest list.

## Ad-hoc badges

The guest list page includes a form for creating badges for walk-in attendees not on the Lu.ma guest list.
