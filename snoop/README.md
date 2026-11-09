# Snoop by Badger

Snoop leaves a trace of every Google Meet that mark@sreday.com is in. It lives inside Snout at **https://scan.sreday.com/snoop**, the same way Snug lives at `/badger`. It shares Snout's binary, its deploy and its admin password, and has a "Snoop" button on Snout's admin page. Each trace has:

- **Notes you can act on:** a summary, plus next steps you can tick off.
- **The full transcript.**

Every call gets a Google Doc in the `Snoop` folder on Drive and an entry on `/snoop/admin`. There you can search all calls and see every open next step.

Once Snoop is running it uses only free tools. There's no meeting bot and no paid AI.

## How it works

There are two sources, combined per call.

| Source | Covers | Transcript | Notes |
|---|---|---|---|
| **Meet API** (server) | Meets that Mark organises | Meet's own transcript | Gemini "Take notes for me" |
| **Snoop extension** (Chrome) | Any Meet Mark joins in Chrome, whoever hosts it | Meet's live captions | Chrome's built-in AI (Gemini Nano), on the laptop |

When a call has both, the Meet transcript wins and the captions stay as a backup.

The calendar is the master list:

- Every event with a Meet link becomes a trace, even if nothing comes in. The trace then says why: someone else's Meet with no captions (joined from a phone, or not joined), nobody joined, and so on.
- Meets with no calendar event also get traced: ones the Meet API knows about, and ones the extension sees.
- Calls booked through Calendly (the invite says "Powered by Calendly.com") are the sales calls: `/snoop/admin` opens on the **Calendly** tab with the Calendly event type on each row; everything else is on **All other calls**. Both tabs show past calls on the left and upcoming ones on the right, and every open next step is listed under the calls (**Open actions**).
- Each call shows 1-5 pale **topic pills** (`pills.go`, plain rules, no AI): the Calendly answers (sponsor or host, brand, area, budget, company), then the prospectus packages (booth, lead list, keynote, talk slot, logo + swag, food & drinks, T-shirts, ticket pack, meetup, hackathon, workshop), brands and cities found in the title, Gemini's notes and the transcript. E.g. "Hosting in Toronto", "Sponsor SREday NYC", "Booth", "No budget". Calls with only sreday.com/conf42.com people get "Internal".
- Declined and cancelled events, and **empty** calls, are kept but hidden on `/snoop/admin` until you switch on the "Show declined, cancelled & empty" toggle (off by default). Empty = over, and either an ad-hoc Meet with no transcript or a transcript with fewer than 20 real words once greetings and small talk are left out ("hey, sorry, I gotta jump, bye"). Gemini notes with a summary always count as substance, and calendar calls with no transcript stay visible, so a failed capture shows.

Everything runs on its own:

- **Server.** Every 5 minutes it reads the calendar from 7 days back to 48 h ahead. For each of Mark's upcoming Meets it switches on transcripts and Gemini notes (`spaces.patch`). Everyone in the call sees Meet's usual banners.
  - After the call it collects the transcript and Gemini's notes. Speaker IDs become names.
  - It writes the Drive doc, and rewrites it whenever something changes, such as new notes or a ticked step.
  - "Sync now" on `/snoop/admin` does the same straight away.
- **Extension.** It starts in any Meet tab signed in as the traced account, and leaves every other Google account alone.
  - It switches captions on (they're only visible to you) and keeps each line once it stops changing.
  - Lines wait on the laptop and upload every 30 s, so a Wi-Fi drop never loses a line.
  - When you leave, or close the tab, it writes the notes on the laptop and sends them.

Meet changes its page now and then. All the extension's selectors are in `SEL` at the top of `extension/content.js`. If no captions turn up within a minute, the extension shows "!" and `/snoop/admin` says so for that call.

## Setup

1. **Workspace.** The sreday.com plan must include Meet transcripts and Gemini "Take notes for me". Check Admin console → Apps → Google Workspace → Google Meet → Meet video settings → Recording / Transcripts, and → Gemini.
2. **Google Cloud** (project `sreday-badger-scan`):
   - Enable the Google Meet REST API, Google Calendar API and Google Drive API.
   - Create an OAuth client of type Web application, with redirect URI `https://scan.sreday.com/snoop/admin/google/callback`.
   - On the consent screen choose Internal, so only sreday.com accounts can use it.
3. **Add the client to Snout's env** (`GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` in `/opt/badger-scan/env`) and restart. Open https://scan.sreday.com/snoop/admin, click **Connect Google** and sign in as mark@sreday.com. Any other account is refused.
4. **Extension, on each machine** (Mac and the Windows PC):
   - Open `chrome://extensions`, turn on Developer mode, click "Load unpacked" and pick `snoop/extension`.
   - On `/snoop/admin` click **Add device**, then paste the server and token into the extension's options and press **Save + test**.
   - Press **Set up on-device AI** once. The model is a one-off download of about 2 GB, and Chrome needs a click to start it. Check `chrome://on-device-internals` to see whether the laptop supports it.

## Run

Snoop runs as part of Snout (`make scanner && ./snout`, see `snout/README.md`), at `/snoop/admin`. Snout passes in its `ADMIN_USER`/`ADMIN_PASSWORD`, `PUBLIC_URL` and `DATA_DIR`. Snoop's own settings:

| Variable | Default | Description |
|----------|---------|-------------|
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | (off) | OAuth client. Without it, only the extension's captions come in |
| `SNOOP_ACCOUNT` | `mark@sreday.com` | The only Google account Snoop connects to and traces |
| `SNOOP_CALENDAR_ID` | `primary` | |
| `SNOOP_DRIVE_FOLDER` | `Snoop` | Name of the Drive folder, created on the first doc |
| `SNOOP_TZ` | `Europe/London` | Time zone for pages and docs |

If Snout's `PUBLIC_URL` is empty, Snoop uses `https://scan.sreday.com` for the OAuth redirect and doc links.

## Data

`DATA_DIR/snoop/` holds:

- `calls/<id>.json`, one file per call
- `google.json`, the refresh token: root-only, and it's the whole of Snoop's access to the account
- `devices.json`, extension token hashes only
- `secret`

Nothing is deleted on a timer.

## Deploy

Snoop ships with Snout: same build, same `badger-scan` VM, same restart (see the scanner README). The only extra steps are the two Google variables in `/opt/badger-scan/env`.
