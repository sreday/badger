# Badger Scan

A sponsor lead scanner. Sponsor staff scan the QR code on attendee badges. Each scan is matched to the event's Lu.ma guest list, and the sponsor downloads a CSV of everyone they talked to, in the order the conversations happened.

There's no app to install and no per-device fee. Sponsors open a link on any phone or laptop, or plug in a £30 handheld scanner.

## How it works

- The badge QR printed by badger is the attendee's LinkedIn (`linkedin://in/<slug>`), and attendees keep scanning each other's badges to connect on LinkedIn. Badger Scan reads that same QR and matches the slug to the guest whose LinkedIn answer produced it. Both sides go through `pkg.ExtractLinkedIn`, so the match is exact.
- It also accepts:
  - any LinkedIn profile URL;
  - the attendee's Luma ticket QR (`…/check-in/evt-…?pk=g-…`), for people without LinkedIn on their badge;
  - an email address typed on the scanner page.
- Every scan is stored, even one that doesn't match the guest list (a walk-in, or a LinkedIn profile changed after printing). Matching is retried against a fresh guest list when the CSV is exported.
- Scanning the same person twice gives "already scanned at 10:42", not a duplicate row. A badge scan and a ticket scan of one person count as the same person.
- Scans queue on the device and sync when the network is back, so venue Wi-Fi dropping out never loses a lead.

## Run

```bash
make scanner
LUMA_API_KEY=... ADMIN_PASSWORD=... ./badger-scan
```

Open `http://localhost:8081/admin` (user name = `ADMIN_USER`, or anything if unset; password = `ADMIN_PASSWORD`).

## Sponsor links

Every sponsor listed on an upcoming event's page gets a sponsor link automatically. The admin page reads `luma_evt` and `sponsors` from each event folder's `metadata.yml` in the site repos (`SITE_REPOS`) every 10 minutes, or straight away with "Check event pages now". The name comes from the logo file (`jug-amsterdam.png` → "JUG Amsterdam"); add `name:` to a sponsor in `metadata.yml` to set it exactly.

- Every link gets its own 4-digit PIN straight away, unique across all links. A PIN is only given out again once its old link has been idle for 100 days.
- **New PIN** replaces it and signs out every phone that used the old one.
- A sponsor taken off the page loses its link only while it was never opened and has no leads. Links in use are never removed.
- Sponsors not on the page yet can be added by hand.

## Phases

Each link follows its event's Luma times; nothing to switch by hand.

| Phase | When | What the sponsor can do |
|---|---|---|
| Test | until 6h before the start | Everything, for real (matching, ratings, notes, CSV, email). Test scans live in memory only and disappear 60 minutes after they're made. |
| Live | 6h before the start → 6h after the end | Scan. The list starts empty. |
| Closed | after that, for 90 days | Look at the list, download or email the CSV. Scans made offline before closing are still accepted for a day. |
| Deleted | 90 days after closing | The leads are deleted from disk; the link says so. |

Scans are saved on the phone first and sent when there's signal, and a service worker lets the scanner page reopen with no signal.

| Variable | Default | Description |
|----------|---------|-------------|
| `LUMA_API_KEY` | (required) | Lu.ma calendar API key; stays on the server |
| `ADMIN_PASSWORD` | (required) | Password for `/admin` |
| `ADMIN_USER` | (any) | User name for `/admin`; empty accepts any name |
| `FULL_ANSWERS` | off | `1` adds the phone number and every other registration answer to the lead card and CSV. Only turn on once the T&C cover it |
| `SMTP_USER`, `SMTP_PASSWORD` | (off) | Gmail account + app password for "Email CSV"; the button is hidden without them |
| `SMTP_ADDR` | `smtp.gmail.com:587` | SMTP server |
| `MAIL_REPLY_TO` | `hello@sreday.com` | Reply-To on the emails |
| `SITE_REPOS` | sreday, llmday, platformday, PEC | Comma-separated GitHub `owner/repo`s whose event pages list sponsors |
| `PUBLIC_URL` | (from request) | Base for sponsor links, e.g. `https://scan.sreday.com` |
| `DATA_DIR` | `data` | Where sponsors and leads are stored |
| `HOST` | `0.0.0.0` | Listen address |
| `PORT` | `8081` | Listen port |
| `LUMA_BASE_URL` | `https://api.lu.ma` | Only for testing against a fake Luma |

## Sponsor page

`/s/<token>` asks for the PIN once per device, then shows:

- **Camera.** Tap "Start camera" and point it at a badge. It uses the browser's built-in QR detector where available (Chrome, Android) and the bundled jsQR otherwise (iPhone). It ignores the badge's WiFi QR.
- **Hardware scanner.** Any USB or Bluetooth "HID" 2D barcode scanner works with no setup: it types the QR into the page and presses Enter. Set the scanner to a US keyboard layout if you can; the parser tolerates the usual layout mangling anyway.
- **Hot / Warm / Cold and a note** for each lead.
- **Download CSV** at any time.

## Data

Everything is plain files in `DATA_DIR`:

- `sponsors.json`: one entry per sponsor link (token, PIN, event, and whether it came from the event page).
- `events/<event_id>/leads.jsonl`: an append-only log of scans and rating/note updates.
- `secret`: the cookie signing key, created on first run.

Back up the folder and you've backed up everything. It contains attendee personal data, so keep it off git and off shared drives.

CSV columns: `Scanned At, First Name, Last Name, Email, Job Title, Company, LinkedIn, Rating, Note, Scanned By`. Times are in the event's time zone.

## Consent

Add one line to the Luma registration terms: *"Sponsors may scan your badge to receive your name, title, company, email and LinkedIn."*

## Deploy for free (GCP e2-micro)

1. Create an **e2-micro** VM in `us-central1`, `us-east1` or `us-west1`, with Debian and a 30 GB standard disk. That stays within Google Cloud's Always Free tier.
2. Copy `badger-scan-linux-amd64` from the release to `/opt/badger-scan/badger-scan`.
3. Add `/etc/systemd/system/badger-scan.service`:

   ```ini
   [Unit]
   Description=Badger Scan
   After=network-online.target

   [Service]
   WorkingDirectory=/opt/badger-scan
   ExecStart=/opt/badger-scan/badger-scan
   Environment=HOST=127.0.0.1 PORT=8081 PUBLIC_URL=https://scan.sreday.com
   EnvironmentFile=/opt/badger-scan/env
   Restart=always

   [Install]
   WantedBy=multi-user.target
   ```

   `/opt/badger-scan/env` (mode 600) holds `LUMA_API_KEY=…` and `ADMIN_PASSWORD=…`.
4. Install Caddy and set `/etc/caddy/Caddyfile` to `scan.sreday.com { reverse_proxy 127.0.0.1:8081 }`. Caddy gets the HTTPS certificate itself. HTTPS is required, because phones only open the camera on secure pages.
5. Point a DNS-only (grey cloud) `A` record for `scan.sreday.com` at the VM.
6. Back up nightly with something like `tar czf ~/backup-$(date +%F).tgz -C /opt/badger-scan data`.
