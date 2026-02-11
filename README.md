# Badger

Conference badge generator and printer. Integrates with the Lu.ma event API to fetch guest lists, generates badge images with name/title/company text and QR codes (LinkedIn + WiFi), and prints to Brother QL label printers via CUPS.

Single static binary — no runtime dependencies.

## Quick start (Mac M1+)

```bash
VERSION=v0.0.1rc3
curl -LO https://github.com/sreday/badger/releases/download/${VERSION}/badger-darwin-arm64
chmod +x badger-darwin-arm64
./badger-darwin-arm64
```

## Quick start (Mac Intel)

```bash
VERSION=v0.0.1rc3
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

## Ad-hoc badges

The guest list page includes a form for creating badges for walk-in attendees not on the Lu.ma guest list.
