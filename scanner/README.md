# Snout by Badger

Snout is a sponsor lead scanner. Sponsor staff scan the QR code on attendee badges. Each scan is matched to the event's Lu.ma guest list, and the sponsor downloads a CSV of everyone they talked to, in the order the conversations happened.

There's no app to install and no per-device fee. Sponsors open a link on any phone or laptop, or plug in a £30 handheld scanner.

## How it works

- The badge QR printed by badger is the attendee's LinkedIn (`linkedin://in/<slug>`), and attendees keep scanning each other's badges to connect on LinkedIn. Snout reads that same QR and matches the slug to the guest whose LinkedIn answer produced it. Both sides go through `pkg.ExtractLinkedIn`, so the match is exact.
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
LUMA_API_KEY=... ADMIN_PASSWORD=... ./snout
```

Open `http://localhost:8081/admin` (user name = `ADMIN_USER`, or anything if unset; password = `ADMIN_PASSWORD`).

## Sponsor links

Every paying sponsor listed on an upcoming event's page gets a sponsor link automatically. The admin page reads `luma_evt` and `sponsors` from each event folder's `metadata.yml` in the site repos (`SITE_REPOS`) every 10 minutes, or straight away with "Refresh Registered Sponsors". Partners don't get a link: a logo listed in a site repo's `partners.yaml` (`non_sponsor_orgs`, `community_partners`, `sister_conferences_job_boards`, `minor_companies`, `hidden_duplicates`) is a partner, the same split the websites use. The name comes from the logo file (`jug-amsterdam.png` → "JUG Amsterdam"); add `name:` to a sponsor in `metadata.yml` to set it exactly.

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
| Closed | after that, for 12 months (90 days for events before 2027) | Look at the list and the dashboard, download or email the CSV. Scans made offline before closing are still accepted for a day. |
| Deleted | 12 months (or 90 days) after closing | The leads are deleted from disk; the link and the dashboard say so. |

Events starting before 1 January 2027 (`longKeepFrom` in `store.go`) keep 90 days, because their attendees registered under T&C that promised deletion 90 days after the event.

Scans are saved on the phone first and sent when there's signal.

**Offline mode** (scanner and dashboard): venue Wi-Fi is usually choppy rather than off, so a request that hangs counts as offline. The service worker is network first, but serves the saved page after 3 s without an answer, or at once if the server returns 5xx; online pages are never stale. Each page keeps its last list from the server on the device and shows "Offline mode · last updated 14:32 (8 min ago)"; that time is saved per page, so a refresh brings back the same view and note. Phone scans and dashboard ratings and notes wait in a queue on the device and sync when the connection is back ("Back online · syncing 3"). Downloads need the server, so offline the dashboard says so instead of starting a failed download. A device must have opened its link online once. The shared piece is `templates/offline.html`.

**Cloud Badger offline** (`/badger`, `badger2/app`): its own service worker (`/badger/sw.js`) keeps the event page (8 s to answer, since the guest list waits on Luma) and every badge picture (3 s). While online, the event page saves each guest's badge on the laptop, two at a time: the missing ones, and all of them again at most every 30 minutes. With no connection a print uses the saved picture (single, Print all and ticket scans alike); a walk-in waits on the laptop and prints by itself when Badger answers again (also after a reload). The note reads "Offline mode · guest list from 17:21 (8 min ago) · 450 of 456 badges ready to print · 1 walk-in waiting"; a guest list opened from the saved copy while online says "reload the page for the latest". Open the event page online on the printing laptop before doors open.

| Variable | Default | Description |
|----------|---------|-------------|
| `LUMA_API_KEY` | (required) | Lu.ma calendar API key; stays on the server |
| `ADMIN_PASSWORD` | (required) | Password for `/admin` |
| `ADMIN_USER` | (any) | User name for `/admin`; empty accepts any name |
| `FULL_ANSWERS` | off | `1` adds the phone number and every other registration answer to the lead card and CSV. Covered by the T&C since 2026-10-26 |
| `SMTP_USER`, `SMTP_PASSWORD` | (off) | Gmail account + app password for "Email CSV"; the button is hidden without them |
| `SMTP_ADDR` | `smtp.gmail.com:587` | SMTP server |
| `MAIL_REPLY_TO` | `mark@sreday.com` | Reply-To on the emails |
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

## Ratings and notes: everyone has their own take

Nobody on a sponsor's team has the final say on a lead. Each person's rating and note is their own take, named after them ("Scanning as" on the phone, `Dashboard` on the dashboard), and only they can change it. The lead's rating is the team's: the rating when everyone agrees, `split` when they don't. The marketing person, or their AI, makes the call.

- **Who's scanning?** suggests the full names of the sponsor's registered people as you type (`/s/<token>/team`: names only, same company / email-domain match as Hot intros), so two Marks pick "Mark Smith" and "Mark Grunner" and stay two people; a first name shared by several colleagues gets a hint. Anyone can still type any name. A phone that typed only a first name matching a colleague is offered the full name once. The dashboard shows full names for first-name-only takes when there's exactly one match.
- Each phone sees the whole team's feed but only its own rating and note: no colleague's rating to sway it. The server leaves the others' takes out of what phones get.
- The dashboard shows everyone's ratings as chips, a `split` badge, and notes as "Mark: …" lines; only the `Dashboard` take is editable. Filters show a lead under every rating anyone gave it.
- The CSV has `Rating` (the team's), `Ratings` ("Mark: hot; David: cold") and `Note` ("Mark: … | David: …").
- **Scanning history** (`/s/<token>/history.csv`, on the dashboard): every scan, rating and note in order, with a Log column like `10:42 Jamie marks John Smith (Acme) as cold`, to sort out disagreements by hand or with AI.

Kept simple on purpose (anything more and nobody uses it):

- **Dashboard:** Rating shows who rated what (`Mark 🔥` `David ❄️`) and the dashboard's own pill; Notes lists every note on its own line, `Dashboard: ...` first (editable), then the phones' in the order they were written.
- **CSV:** `Rating` is who rated what ("Mark: hot; David: cold"), `Note` one line per person, the dashboard's first.
- **Hot intros** (admin, per event): every lead someone rated hot, with who on the sponsor's team did, for the organizers to introduce them after the event. The phone name ("Peter") is matched to the event's registrants from that sponsor (company is the sponsor's name, or email on the sponsor's website domain from the event page; same first name, or full name if the phone has one): one match fills Rater name and Rater email, none or several go to Check for a manual look.

On disk, a take is a `take` record with its `author`; the old single `update` records count as the first scanner's take.

## Sponsor-ready leads

Every event on the admin page has **Download pre-conference leads**, which becomes **Download post-conference leads** once the event's Luma end time has passed: the event's approved Luma registrations, pulled live, in the sheet sponsors get. Columns `First name, Last name, Email, Job title, Company, LinkedIn`, sorted by last name (accent-aware), LinkedIn as `https://linkedin.com/in/<slug>`, UTF-8 with a BOM so Excel keeps accented names. The file is `<Event> - sponsor leads (pre-conference).csv` or `(post-conference).csv`.

The post-conference file adds **Check-in** (`attendance.go`):

- `yes - morning` / `yes - afternoon`: Luma's check-in time, before or after noon in the event's time zone;
- `no, but was there`: no check-in, but a speaker whose talk is on the event page (`_db/talks.csv`, status talk / keynote / workshop / confirmed, so not `noshow` or a draft; matched by LinkedIn, else name), someone registered by a sponsor (email domain is the sponsor's site domain, or the company is the sponsor's name; their bosses sent them), or an organizer (a conference name as their company);
- `no`: everyone else.

People who registered twice (work and personal email) get one row: same name (a first initial counts) plus the same LinkedIn, company or work email domain. The row is their most complete registration (all fields, a full first name, a real LinkedIn, a work email), checked in or scanned if either registration was. A shared LinkedIn with a different name, or a placeholder like `non`, never merges two people.

Sponsors who bought the leads (admin: "They purchased leads") get the same list from their dashboard, with their team's `Rating`, `Note` and `Scanned by` added once scanning is live. Before that (the Test phase) it's the registrations only, without those empty columns.

The event page is found in the site repos by its `luma_evt`, at the top level or under `past/`; partners don't count as sponsors. Every download is logged. Covered by T&C 16.1(b)(vii) (limited personal data to official sponsors): phone numbers and registration answers stay out.

## Sponsor dashboard

`/s/<token>/dashboard` is the same link with `/dashboard` on the end, for a computer. It takes the same PIN (once per browser) and shows:

- the leads, newest first, refreshing every 5 seconds while the page is open (once a minute after the event);
- totals for Hot / Not sure / Cold, and how many each person on the team scanned;
- search and a rating filter;
- **Download CSV**.

Sponsor-facing pages and emails never mention retention or explain how leads are handled; that stays in this README.

Ratings and notes can be changed here too: the dashboard saves them through the same `/sync` update the phones use, so changes show up on every phone and dashboard within a few seconds. Editing stops when scanning closes, as on the phones. It works as long as the leads exist. The admin card has **Open Sponsor Dashboard ↗**.

Every sponsor page is unlisted: `X-Robots-Tag: noindex`, `Referrer-Policy: no-referrer` (the secret link never leaks to sites the sponsor clicks through to), no framing, and `/robots.txt` disallows everything. Every CSV download is logged with who took it (sponsor or admin).

## Data

Everything is plain files in `DATA_DIR`:

- `sponsors.json`: one entry per sponsor link (token, PIN, event, and whether it came from the event page).
- `events/<event_id>/leads.jsonl`: an append-only log of scans and rating/note updates.
- `secret`: the cookie signing key, created on first run.

Back up the folder and you've backed up everything. It contains attendee personal data, so keep it off git and off shared drives.

CSV columns: `Scanned At, First Name, Last Name, Email, Job Title, Company, LinkedIn, Rating, Note, Scanned By`. Times are in the event's time zone.

## Consent

The conference T&C cover it in clause 16.1(b)(viii) (updated 2026-10-26 on SREday, LLMday, PLATFORMday, DevOps Not Dead and PEC): sponsors receive name, contact details (email and phone), title, company, LinkedIn, registration answers and their own notes and rating; scanning is optional; data is stored in the US. That covers `FULL_ANSWERS=1`.

The T&C name no retention period (since 2026-10-28): Snout keeps scan data "for as long as it is needed to make it available to the sponsor, and then deletes it" (GDPR allows stating the criteria instead of a period). Once the sponsor has the data, the sponsor's own privacy policy applies, so their copy is their responsibility. Events before 2027 keep 90 days in Snout, because the 2026-10-26 wording promised that to people who registered under it.

## Deploy for free (GCP e2-micro)

1. Create an **e2-micro** VM in `us-central1`, `us-east1` or `us-west1`, with Debian and a 30 GB standard disk. That stays within Google Cloud's Always Free tier.
2. Copy `snout-linux-amd64` from the release to `/opt/snout/snout`.
3. Add `/etc/systemd/system/snout.service`:

   ```ini
   [Unit]
   Description=Snout by Badger
   After=network-online.target

   [Service]
   WorkingDirectory=/opt/snout
   ExecStart=/opt/snout/snout
   Environment=HOST=127.0.0.1 PORT=8081 PUBLIC_URL=https://scan.sreday.com
   EnvironmentFile=/opt/snout/env
   Restart=always

   [Install]
   WantedBy=multi-user.target
   ```

   `/opt/snout/env` (mode 600) holds `LUMA_API_KEY=…` and `ADMIN_PASSWORD=…`.
4. Install Caddy and set `/etc/caddy/Caddyfile` to `scan.sreday.com { reverse_proxy 127.0.0.1:8081 }`. Caddy gets the HTTPS certificate itself. HTTPS is required, because phones only open the camera on secure pages.
5. Point a DNS-only (grey cloud) `A` record for `scan.sreday.com` at the VM.
6. Back up nightly with something like `tar czf ~/backup-$(date +%F).tgz -C /opt/snout data`.
