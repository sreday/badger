# Sniff by Badger

Live questions and feedback for every session of an event: one QR for the whole event on every speaker's last slide, the audience asks, votes and rates on their phones, and each speaker follows along on their own page.

Sniff is the Go package `sniff` (`github.com/sreday/badger/sniff`). It has no binary of its own: Snout ([`snout/`](../snout/README.md)) mounts it on scan.sreday.com with `sniff.New(cfg, host)` and `Register(mux)`, the way it hosts Snoop at `/snoop` and Snug at `/snug`. Snout passes in its data dir, Luma key, public URL, its admin login (for the organizer pages), its clock and its cached Luma guest lists (for name matching).

## URLs

| URL | Who | |
|---|---|---|
| `GET /sniff` | the team (Snout's admin password) | events, and **Download all questions** per event (`/admin/sniff/<luma event id>/feedback.csv`) |
| `/sniff/<event secret>` | anyone with the link (never listed: robots.txt, `noindex`, `no-referrer`) | the speakers' 4-step tutorial (PIN 1111) and every speaker's card: **Open dashboard**, Slide, QR. Search + sort (as website / A–Z). The old `/admin/sniff/<luma event id>` redirects here |
| `/q/<code>` | the audience, and the presenter with the PIN | one talk: ask, vote, rate; with the PIN the same page becomes the presenter view (♥ and Hide). `feed`, `ask`, `vote`, `rate`, `del`, `pin`, `mod`, `face.jpg` |
| `/sniff/<event secret>/<name>` | the speaker (PIN 1111) | the desktop page: every question and rating with who asked, ♥ / Hide, CSV. `qr.png` and `slide.png` under it need no PIN |
| `/sniff/<token>/...` | the speaker page's own calls (PIN) | `feed`, `mod`, `export.csv`, `pin`; `GET /sniff/<token>` redirects to the name link |
| `/q/<event code>`, `/sniff/<first name>` | old links | "scan the QR on the speaker's slide"; the talk's `/q/<code>` (never the secret) |

Every page except the speaker picture (`face.jpg`) is unlisted (`X-Robots-Tag: noindex`, `Referrer-Policy: no-referrer`, no framing).

## How it works

**One QR per talk, one PIN for every presenter.** The audience and the speaker scan the same QR (on the talk's slide), so people only ask about and rate the talk in front of them. The speakers' onboarding email has the event link and the PIN 1111: easy to remember, not what an attendee would guess, and all it gives on the phone is hearts and hides (nothing to edit or download).

- **Event page:** `/sniff/<event secret>` (from `/sniff`, or the Sniff Q&A button in `/snout`). Each event gets a random 10-character secret (lowercase, at least two digits, no look-alikes) the first time it's opened, kept in `sniff_secrets.json`. A card per speaker (`shardcard` in `sniff_parts.html`: the website's headshot, LinkedIn, name, talk, phase; initials when there's no photo), five a row, with **Open dashboard**. Search (`/`) by speaker, talk or company; sort as on the website or A–Z, cards gliding to their place (the teasers' FLIP). Sessions come from the event page's schedule (synced on every visit, 10-minute cache): the `#live-data` JSON on `<base_path>/<event folder>/live/`, else `_db/talks.csv`. Slots without a speaker never become sessions (`notSpeaker` in `sniff_schedule.go`). Names: the speaker's first name (`/sniff/k7mq4x2pw9/grace`); another Grace at the event gets `grace-hopper`, the same Grace twice `grace-2`, in the order sessions were made, so links never move. This page never mentions the other Badger apps.
- **Phone, audience:** `/q/<code>`. The first visit asks "Your name" once per event (or Stay anonymous), then one card: the question and the 3 stars (a tap sends the rating; a comment is optional). The room's questions below, ▲ votes, hearted ones on top. **I'm the presenter** at the bottom opens the PIN sheet. About 5.6 KB gzipped, system fonts, no images, an offline outbox; plain forms work without JavaScript.
- **Phone, presenter:** the PIN leaves a year-long cookie (its value follows the PIN) and the same `/q/<code>` turns into the presenter view: every question, newest first, hearted ones on top; ♥ and Hide (`POST /q/<code>/mod`: `important`, `unimportant`, `hide`, `show`, nothing else).
- **Speaker's desktop page:** `/sniff/<secret>/<name>`, behind the PIN. On top the 4-step tutorial (Snout's sponsor dashboard setup, hide/show remembered): 1 the talk's own QR + Download slide / QR, 2 the PIN, 3 answer live, hearts on top, 4 who asked, below. Questions and feedback with who asked: the attendee the typed name matched (first and last name, job title, company, LinkedIn, email: T&C 16.1(b)(ix) since November 11 2026). ♥ and Hide; **Download questions + feedback (.CSV)** any time: Session, Type, Time, Question / comment, Stars, Votes, Hearted, Asked as, First name, Last name, Email, Job title, Company, LinkedIn (the sponsor-ready fields), UTF-8 BOM. The team's "Download all questions" on `/sniff` is the same for every talk.
- **Filter** (`sniff_filter.go`): a question with a slur, a sexual word or an insult, in the text or the name, is never stored and never reaches the speaker; the asker's phone is told (`{"filtered":true}`) and shows it in their own list with a "You can do better than this" pill; a rating's comment like that keeps its stars and loses its words. Whole words after folding accents, leetspeak and stretched letters; the worst few also inside words or spelled out. Words with innocent tech or English uses (kill, dump, naked pods, niggle, Niger, stupid question, ugly hack, boring technology) pass. The asker's phone keeps showing its own questions, so a dropped one looks sent.
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
