# Sniff by Badger

Live questions and feedback for every session of an event: one QR for the whole event on every speaker's last slide, the audience asks, votes and rates on their phones, and each speaker follows along on their own page.

Sniff is the Go package `sniff` (`github.com/sreday/badger/sniff`). It has no binary of its own: Snout ([`snout/`](../snout/README.md)) mounts it on scan.sreday.com with `sniff.New(cfg, host)` and `Register(mux)`, the way it hosts Snoop at `/snoop` and Snug at `/snug`. Snout passes in its data dir, Luma key, public URL, its admin login (for the organizer pages), its clock and its cached Luma guest lists (for name matching).

## URLs

| URL | Who | |
|---|---|---|
| `GET /sniff` | the team (Snout's admin password) | events to open Sniff for |
| `/sniff/<event secret>` | anyone with the link (never listed: robots.txt, `noindex`, `no-referrer`) | every session's links, the event's QR (`qr.png`), slide (`slide.png`), CSV (`feedback.csv`), add / refresh / reset / hide (`POST /sniff/<secret>/...`). The old `/admin/sniff/<luma event id>` redirects here; its actions still work with the admin password |
| `/q/<event code>` | everyone | the event's one QR: every speaker's picture and name |
| `/q/<code>` | the audience | ask, vote, rate; `feed`, `ask`, `vote`, `rate`, `del`, `pin`, `face.jpg` |
| `/sniff/<event secret>/<speaker name>` (older: `/sniff/<first name>`, `/sniff/<token>`) | the speaker (PIN 1111) | questions and feedback live; `feed`, `pin`, `mod`, `qr.png`, `slide.png`, `export.csv` (under the token link) |

Every page except the speaker picture (`face.jpg`) is unlisted (`X-Robots-Tag: noindex`, `Referrer-Policy: no-referrer`, no framing).

## How it works

Sniff gives an event **one QR for everyone** (on every speaker's last slide) for questions and quick feedback, and each speaker a private page that shows everything live.

- **Everyone:** `/q/<event code>` (made once, for good, the first time the organizer page opens; same alphabet as session codes, never clashing). The page title is "<event> - Questions and Answers", then every speaker's naked picture and name (no talk titles); a tap opens that speaker's `/q/<code>`. Speakers tap their own face, then **I'm the speaker** (top right, or at the bottom) and the PIN **1111** opens their page (`POST /q/<code>/pin`). The PIN is the same for every speaker at every event, on purpose: easy to share and remember, and someone in the room guessing it only gets to read the questions. That phone remembers it: next time the button says **Your questions →**. Pictures (`/q/<code>/face.jpg`, `sniff_faces.go`): the site's `speakers/<Who>.png`, background kept but squared: the repo's `home/assets/images/headshot-circle.png` colour carries on past the circle's edge, cropped from the top of the head, a small JPEG cached a day. Never a circle.
- **Organizers:** `/sniff/<event secret>` (from the list on `/sniff`, or the Sniff Q&A button on each event in `/snout`). Each event gets a random 10-character secret (lowercase, at least two digits, no look-alikes) the first time its page opens from `/sniff`; the page and its buttons need no password, the link is the key; it never changes (`sniff_secrets.json`, event id -> secret). Speaker pages hang under it by name: `/sniff/k7mq4x2pw9/grace`: the speaker's first name (co-speakers: the first one's), accents folded. Another Grace at the same event gets `grace-hopper`, the same Grace twice (talk + workshop) gets `grace-2`; names are handed out in the order sessions were made, so they never move. A session without a speaker uses its title. The header is just "<event> - Questions and Answers"; then the event's QR, its slide, copy link, copy speaker links (tab-separated) and the CSV; then a card per speaker: picture, name, counts, open/copy the speaker link (`/sniff/<secret>/<name>`), hide. Sessions come from the event page's schedule: the `<script id="live-data">` JSON on `<base_path>/<event folder>/live/` (times, rooms, titles, speakers), else `_db/talks.csv` (no times). Slots without a speaker never become sessions: an empty speaker field, one equal to the title ("Coral Keynote"), or one with a slot word (opening, welcome, intro, keynote, sponsor, panel, TBA, break, lunch, networking... `notSpeakerWords` in `sniff_schedule.go`); one made before the rule is hidden on the next sync (Show again brings it back). Sessions added by hand are never filtered. Each session gets a 4-letter code (`/q/K7RM`, no look-alike letters) a speaker link (`/sniff/<token>`, 128-bit); the speaker's QR and slide open the event's one link. **Codes never change**: a talk whose title (and so its page key) changed keeps its code when the speaker's name finds it; a talk that left the schedule is flagged "Not on the schedule", and its QR keeps working. Per-session QR codes printed before the one-link change keep working.
- **Audience:** `/q/<code>`. Ask, vote (▲, one per phone), rate the talk (1-3 stars + one optional comment, one rating per phone, the latest counts) and delete your own question. One HTML request of about 5 KB gzipped, system fonts, no images; plain forms work without JavaScript. Posts wait in a localStorage outbox until the server has them (resends are no-ops); the list polls `/q/<code>/feed?v=` every 10 s while visible and gets `204` with no body when nothing changed.
- **Speaker:** `/sniff/<event secret>/<name>` (`/sniff/k7mq4x2pw9/grace`). Older links keep working: `/sniff/<first name>` (`/sniff/grace`; any case) and `/sniff/<token>`. It asks for the PIN (1111) once per phone (`POST /sniff/<link>/pin`, or under **I'm the speaker**): a year-long cookie whose value follows the PIN, then every speaker's page opens on that phone. Without it the page is only the PIN box and the feed, CSV, QR and slide answer 403. If you know, you know: attendees don't, and one who guesses it only reads questions. New sessions no longer get a `/sniff/<first name>` link (guessable); the ones already given keep working (the open session wins), and so do the `/sniff/<token>` links. A speaker without a Latin-letter name gets the session title as the name. Questions (Top/New, **☆ Important** (on top of the list in light gold, also first in Stage mode), Answered, Hide, Show hidden, **Stage mode**: the top open question huge on a dark screen), Feedback (average, spread, comments), Share (QR, link, the 1920x1080 slide in the conference colour, CSV). Polls every 3 s; new questions slide in.
- **Quiet filter** (`sniff_filter.go`): a question with a slur, a sexual word or an insult, in the text or the name, is thanked like any other ("Thank you for asking!") and never stored; a rating's comment like that keeps its stars and loses its words. Whole words after folding accents, leetspeak and stretched letters; the worst few also inside words or spelled out. Words with innocent tech or English uses (kill, dump, naked pods, niggle, Niger, stupid question, ugly hack, boring technology) pass. The asker's phone keeps showing its own questions, so a dropped one looks sent.
- **Names:** the name someone types (remembered on their phone for the whole event) is matched to the approved Luma guests and the session's speakers (`sniff_match.go`): one word = the only guest with that first name; two or more = the only one with that first and last name (middle names skipped), or a last initial ("Peter S"); accents and case don't matter. Exactly one match: the speaker sees name, job title, company and LinkedIn; otherwise just what was typed. The room only ever sees the typed first name, and the asker never learns whether it matched, so the page says nothing about who registered.
- **Privacy:** an anonymous post stores no name, guest or phone key. Votes and ratings use random keys the phone makes per session, so nothing links posts together. Rate limits (4 questions a minute per phone, 60 votes, 10 ratings; 120 questions a minute per session; 600 posts a minute per IP, because a venue shares one IP) live in memory only. Handlers log nothing about who posted what. A question its author deletes is also removed from the file. Sponsors never see Sniff data.
- **Phases** follow the event's Luma times, like sponsor links: test until 6 h before the start (posts in memory only, tagged TEST, gone at go-live), live until 6 h after the end, then posting stays open one more day (feedback on the way home); after that the pages are read-only.
- **Data:** `sniff_sessions.json`, `sniff_secrets.json` and `events/<event>/sniff.jsonl` (append-only; its own lock, so the room never slows the scanners).

## Where its data lives

All in Snout's `DATA_DIR`, the same paths as before Sniff had its own package:

- `sniff_sessions.json`: every session (code, speaker link, slug, PIN, event, title, speaker, times).
- `sniff_events.json`: each event's one code (`/q/<event code>`).
- `events/<event id>/sniff.jsonl`: the posts, append-only, next to the event's `leads.jsonl` but with their own lock.
- `faces/`: the speakers' pictures, cached.

## Code

- `server.go`: `Config`, `Host`, `New`, `Register`, the events list (`GET /sniff`).
- `sniff_links.go`: event secrets and speaker names: `/sniff/<secret>`, `/sniff/<secret>/<name>`.
- `sniff.go`: the audience, speaker and organizer handlers, rate limits, QR, slide and CSV.
- `sniff_store.go`: sessions and posts on disk.
- `sniff_schedule.go`, `sniff_shard.go`, `sniff_faces.go`: the event page's schedule, speaker cards and pictures (from the site repos, via `sites/`).
- `sniff_match.go`: typed names to Luma guests and speakers.
- `sniff_filter.go`: the quiet filter.
- `templates/sniff_*.html`: the pages; the shared header comes from `ui/brand.html`.
