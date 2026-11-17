# Snoop by Badger

Snoop leaves a trace of every Google Meet that mark@sreday.com is in. It lives inside Snout at **https://scan.sreday.com/snoop**, the same way Snug lives at `/snug`. It shares Snout's binary, its deploy and its admin password, and has a "Snoop" button on Snout's admin page. Each trace has:

- **Notes you can act on**, written by Claude (the "Snoop: read the calls" routine on Mark's subscription: started by Snoop when calls wait, and hourly as the safety net; instructions in `claude.go`):
  - **Brief first**: a one-line TLDR, points of a few words and one-line steps, each with the full version behind "Read more", so the page reads in under a minute.
  - **TLDR**: who we met and what they want, with up to 3 links (LinkedIn, website, event), so the page still makes sense months later.
  - **Two sides**: our steps and the guest's, at most 5 each, the most critical for the business first. Only steps marked **important** on the call page reach Needs attention on the dashboard.
  - **What was said**: the key points with their exact conditions, checked line by line against the transcript.
  - **Read in context**: Claude gets the house rules, the earlier calls with the same people, Mark's mail with them (through Snap), and can look up his mail and other calls for similar situations (`/api/claude/mail`, `/api/claude/calls`). Gemini's notes are treated as unreliable.
  - **Correct the notes**: say what the notes got wrong on the call page; Claude re-reads the call with it and carries it as a lesson into every later call.
  - **House rules**: who the company is, how it works and which topics stay out of Snoop are private, so they live on the server in `DataDir/snoop/house.md` and go out with every read. Claude cuts or rewords transcript lines the rules exclude. While the routine is alive, a call Claude hasn't read yet shows no transcript and no automatic notes anywhere (page, Drive doc, search, Snap); the raw material stays in Meet's own doc.
  - **A proofread transcript**: names speech-to-text got wrong are corrected, empty lines ("gimme a minute") and filler ("uh", "yyy") are left out. It is folded on the page, loads on "Show transcript", and has its own search. The stored lines stay word for word, as does Meet's own doc.
- **The full transcript.**

Every call gets a Google Doc in the `Snoop` folder on Drive and an entry on `/snoop`. There you can search all calls and see every open next step.

Once Snoop is running it uses only free tools. There's no meeting bot and no paid AI.

## How it works

There are two sources, combined per call.

| Source | Covers | Transcript | Notes |
|---|---|---|---|
| **Meet API** (server) | Meets that Mark organises | Meet's own transcript | Gemini "Take notes for me" |
| **Snoop extension** (Chrome) | Any Meet Mark joins in Chrome, whoever hosts it | Meet's live captions | Chrome's built-in AI (Gemini Nano), on the laptop |

When a call has both, the Meet transcript wins and the captions stay as a backup.

**Other languages.** Meet writes transcripts and Gemini notes in a few languages only (English, French, German, Italian, Japanese, Korean, Portuguese, Spanish); live captions cover many more, Polish among them. Every call page has a **Language** menu: pick the language and Snoop takes the words from the captions the extension read, and leaves Meet's transcript and Gemini's notes of that call aside. It can be set before or after the call, and the calls still to come in the same recurring series follow. In the Meet itself the captions must be set to that language (the CC settings), and Chrome with the extension must be in the call. **Download extension** next to the menu (and in the Chrome extension menu) hands out the extension of the running build as a zip.

The calendar is the master list:

- Every event with a Meet link becomes a trace, even if nothing comes in. The trace then says why: someone else's Meet with no captions (joined from a phone, or not joined), nobody joined, and so on.
- Meets with no calendar event also get traced: ones the Meet API knows about, and ones the extension sees. Such a Meet has no invite, so once it is over Snoop asks Meet who was actually in it: the names show on the call as **Joined**, go into the Drive doc and the search, and the trace is named after the guests ("Meet with Alex Example").
- Calls booked through Calendly (the invite says "Powered by Calendly.com") are the sales calls: `/snoop` opens on the **Calendly** tab with the Calendly event type on each row. **Recurring** has meetings that happen more than once (a repeating calendar event, or the same title with the same people on different dates; this wins over Calendly); everything else is on **Other**. Every tab shows past calls on the left and upcoming ones on the right, and every open next step is listed under the calls (**Open actions**).
- Each call shows 1-5 pale **topic pills** (`pills.go`, plain rules, no AI): the Calendly answers (sponsor or host, brand, area, budget, company), then the prospectus packages (booth, lead list, keynote, talk slot, logo + swag, food & drinks, T-shirts, ticket pack, meetup, hackathon, workshop), brands and cities found in the title, Gemini's notes and the transcript. E.g. "Hosting in Toronto", "Sponsor SREday NYC", "Booth", "No budget". Calls with only sreday.com/conf42.com people get "Internal".
- Declined and cancelled events, and **empty** calls, are kept but hidden on `/snoop` until you switch on the "Show declined, cancelled & empty" toggle (off by default). Empty = over, and either an ad-hoc Meet with no transcript or a transcript with fewer than 20 real words once greetings and small talk are left out ("hey, sorry, I gotta jump, bye"). Gemini notes with a summary always count as substance, and calendar calls with no transcript stay visible, so a failed capture shows.

Everything runs on its own:

- **Server.** Every 5 minutes it reads the calendar from 7 days back to 4 weeks ahead (the Upcoming list). For each of Mark's Meets starting within 48 h it switches on transcripts and Gemini notes (`spaces.patch`). Everyone in the call sees Meet's usual banners.
  - After the call it collects the transcript and Gemini's notes. Speaker IDs become names.
  - It writes the Drive doc, and rewrites it whenever something changes, such as new notes or a ticked step.
  - "Sync now" on `/snoop` does the same straight away.
- **Extension.** It starts in any Meet tab signed in as the traced account, and leaves every other Google account alone.
  - It switches captions on (they're only visible to you) and keeps each line once it stops changing.
  - Lines wait on the laptop and upload every 30 s, so a Wi-Fi drop never loses a line.
  - When you leave, or close the tab, it writes the notes on the laptop and sends them.

Meet changes its page now and then. All the extension's selectors are in `SEL` at the top of `extension/content.js`. If no captions turn up within a minute, the extension shows "!" and `/snoop` says so for that call.

## Setup

1. **Workspace.** The sreday.com plan must include Meet transcripts and Gemini "Take notes for me". Check Admin console → Apps → Google Workspace → Google Meet → Meet video settings → Recording / Transcripts, and → Gemini.
2. **Google Cloud** (project `sreday-badger-scan`):
   - Enable the Google Meet REST API, Google Calendar API and Google Drive API.
   - Create an OAuth client of type Web application, with redirect URI `https://scan.sreday.com/snoop/admin/google/callback`.
   - On the consent screen choose Internal, so only sreday.com accounts can use it.
3. **Add the client to Snout's env** (`GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` in `/opt/badger-scan/env`) and restart. Open https://scan.sreday.com/snoop, click **Connect Google** and sign in as mark@sreday.com. Any other account is refused.
4. **Extension, on each machine** (Mac and the Windows PC):
   - Open `chrome://extensions`, turn on Developer mode, click "Load unpacked" and pick `snoop/extension`.
   - On `/snoop` click **Add device**, then paste the server and token into the extension's options and press **Save + test**.
   - Press **Set up on-device AI** once. The model is a one-off download of about 2 GB, and Chrome needs a click to start it. Check `chrome://on-device-internals` to see whether the laptop supports it.

## Run

Snoop runs as part of Snout (`make snout && ./snout/snout`, see `snout/README.md`), at `/snoop` (the old `/snoop/admin` redirects there). Snout passes in its `ADMIN_USER`/`ADMIN_PASSWORD`, `PUBLIC_URL` and `DATA_DIR`. Snoop's own settings:

| Variable | Default | Description |
|----------|---------|-------------|
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | (off) | OAuth client. Without it, only the extension's captions come in |
| `SNOOP_ACCOUNT` | `mark@sreday.com` | The only Google account Snoop connects to and traces |
| `SNOOP_CALENDAR_ID` | `primary` | |
| `SNOOP_DRIVE_FOLDER` | `Snoop` | Name of the Drive folder, created on the first doc |
| `SNOOP_TZ` | `Europe/London` | Time zone for pages and docs |
| `SNOOP_READ_ROUTINE`, `SNOOP_READ_TOKEN` | (off) | Lets Snoop start the reading routine itself when calls wait for Claude, instead of waiting for the hourly run: the id of "Snoop: read the calls" (`trig_...`, in the routine's address) and the token of an API trigger. The owner adds the trigger once: open the routine, Edit, Trigger, +, API, generate the token. Both can also be pasted on any call page (kept in `DATA_DIR/snoop/read.json`, which wins over the env). Without them nothing is started and the hourly run reads the calls |

If Snout's `PUBLIC_URL` is empty, Snoop uses `https://scan.sreday.com` for the OAuth redirect and doc links.

**Starting the reading routine.** After every sync (every 5 minutes, and right after a correction) Snoop looks at the calls waiting for Claude. When there are some, it starts the routine through its API trigger: at most once every 10 minutes, not within 10 minutes of the routine's last visit, and only once for the same queue, so a start that fails or a call a run can't settle waits for the hourly run instead of being fired at again.

**One run per call.** A call the queue hands out is not handed out again for 15 minutes, so two runs that overlap don't read the same calls. It comes back at once when it changes (new transcript lines, a correction), leaves for good when its answer arrives, and is handed out again after the 15 minutes when the run never answered. "remaining" counts only calls a next fetch would get, so an empty list always comes with 0. The hand-outs are kept in memory: a restart forgets them.

## Data

`DATA_DIR/snoop/` holds:

- `calls/<id>.json`, one file per call
- `google.json`, the refresh token: root-only, and it's the whole of Snoop's access to the account
- `devices.json`, extension token hashes only
- `read.json` and `draft.json`, the ids and API tokens of the reading and drafting routines, when pasted on a call page
- `secret`

Nothing is deleted on a timer.

## Deploy

Snoop ships with Snout: same build, same `badger-scan` VM, same restart (see the scanner README). The only extra steps are the two Google variables in `/opt/badger-scan/env`.

## Snap

Snoop and Snap talk through the `bridge` package, without touching each other's data: see `bridge/README.md` (Resolve in Snap on the Needs attention tiles, the pre-call brief, missed calls that resolve themselves once the email is sent).
