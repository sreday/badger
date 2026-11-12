# Snap by Badger

Snap is Marek's "don't forget, to win" list at **https://scan.sreday.com/snap**. It doesn't list mail: Claude reads the Primary inbox and Snap shows only the few high-stakes threads that are slipping:

- a sponsor or host who said "we'll think about it" and went quiet,
- a host who said "let's touch base in 6 months" (it comes back on that date),
- a speaker who cancelled but whose talk is still in the event's `talks.csv`,
- a speaker's talk change that never reached the site,
- a sponsor, host or partner waiting on Mark for a decision, contract or price.

Core rule (Marek 2026-11-12): if Marek answers everything promptly, Snap has nothing to do. Resolved threads, threads he's actively handling, ball-in-their-court threads with nothing at stake, logistics and notifications are never shown. Old loose ends are: a sponsor who wanted to buy six months ago and never heard back is exactly what Snap is for.

Threads sit on four tabs, **Sponsors** (hosts and venues included), **Speakers**, **Team** and **Others**. The list is on the left. On the right is the picked thread: the loose end, a summary, its recency (last message, who wrote last, who has the ball, follow-up date) and up to three recommended actions. **Copy for Claude** puts an action's prompt on the clipboard, with the thread's subject, id, people and summary, ready to paste into Claude Code. Snap itself never sends, drafts or changes anything in Gmail.

**Done**, **Dead end** and **Snooze** take a thread off the list. A loose end Claude flagged never expires on its own, even past a year; only Done or Dead end closes it. A new message on the thread brings it back.

Snap lives inside Snout, like Snoop. It has the same binary, deploy and admin password.

## Who does what: Snout reads, Claude thinks, on Marek's subscription

No API key and no bill (Marek 2026-11-11: "these flows live inside my Claude subscription").

**Snout, every hour (free):**

1. Lists a year of threads through the Gmail API, with Snap's own Google connection (Gmail read-only, `google.go`). Only the **Primary** tab (`category:primary`): Promotions, Social, Updates and Forums never reach Snap, nor do chats, spam and drafts. Threads that leave the list (moved out of Primary, deleted, past a year) leave Snap too, unless they're an open loose end.
2. Reads only the headers and snippets of threads that are new or changed since the last read (Gmail's history id). It drops:
   - threads with no reply from a person: newsletters, notifications (`List-Unsubscribe`, `Precedence: bulk`, auto-replies, `noreply@`, Luma, Calendly, bookings...), calendar answers ("Accepted:", "Geweigerd:"...), Luma host copies, other people's emoji reactions, and Mark's own outreach that nobody answered.
3. Sorts what's left into tabs by rules (never shown: only Claude's read is) and queues a thread for Claude only once it's **ripe**: someone has waited on Mark **24 hours**, a speaker thread (drop-off, talk change) has sat **48 hours**, or the thread has been quiet **2 weeks** (a deal gone cold). While Mark is replying like a champ, nothing is queued and no tokens are spent. The excerpt is short: who, when, and only the new text of each message (quoted replies and signatures cut), the first message and the last five.
4. Checks every speaker who cancelled (after 48 hours) against the upcoming events' `talks.csv` in the site repos. Once the talk is off the lineup, the loose end closes by itself.

**Claude, every 6 hours (the routine "Snap", Claude Code on Marek's subscription, latest Opus):**

1. `GET https://scan.sreday.com/snap/api/queue` with the bearer token. That returns up to 25 threads, plus the instructions (they live in `prompt.go`, so changing them ships with Snout, not by editing the routine) and how many remain.
2. Reads them, flags only what's at stake (the instructions are strict: "when in doubt, leave it out"; an unflagged thread's answer is just id, history, category and `loose_end: false`), and `POST`s `/snap/api/answers`: `{"model": ..., "threads": [{id, history, category, loose_end, label, why, summary, waiting_on, follow_up_on, priority, company, event, actions, speaker_cancelled, speaker_cancelled_event}]}`.
3. Repeats until the queue is empty, or 8 rounds. A thread Claude has read is not queued again until it changes, so a quiet inbox costs nothing. An answer for a thread that got new mail in the meantime is dropped, and the thread is queued again.

The Claude menu in the header shows the queue, the last answer and the routine's link (**Run now** is on the routine's page).

Why not the Gmail connector inside the routine? Tested 2026-11-11: the routine's safety check flagged posting mail from the connector to an outside server as exfiltration. Here the routine only gets excerpts from Snap and gives answers back to Snap. That passed the check, and the connector's full-thread reads would also cost far more of the subscription.

## Setup

1. **Gmail API** on the Google Cloud project `sreday-badger-scan`. Enabled on 2026-11-11.
2. **Snap's own OAuth client** (not Snoop's). In Google Cloud Console, open `sreday-badger-scan`, then APIs & Services → Credentials → Create credentials → OAuth client ID. Type: Web application. Name: Snap. Authorized redirect URI: `https://scan.sreday.com/snap/admin/google/callback`. Put its ID and secret in `/opt/badger-scan/env` as `SNAP_GOOGLE_CLIENT_ID` and `SNAP_GOOGLE_CLIENT_SECRET`, then restart.
3. **Connect Google in Snap** (Gmail menu, Connect Google) as mark@sreday.com, and allow "Read your email". The token is Snap's own (`DATA_DIR/snap/google.json`), separate from Snoop's.
4. **Network:** the routine's cloud environment must be allowed to reach `scan.sreday.com` (claude.ai/code, the environment's settings, network access: add the host). Otherwise its proxy refuses the connection ("CONNECT tunnel failed, response 403").
5. **The routine** holds the token from `/opt/badger-scan/data/snap/token` (made on Snap's first start) in its prompt. `SNAP_ROUTINE` in Snout's env is the routine's page, for the Claude menu.

| Variable | Default | |
|---|---|---|
| `SNAP_GOOGLE_CLIENT_ID`, `SNAP_GOOGLE_CLIENT_SECRET` | (none) | Snap's own OAuth client |
| `SNAP_EVERY` | `1h` | How often Snout reads Gmail |
| `SNAP_ROUTINE` | claude.ai/code/routines | The Claude routine's page |
| `SNAP_TEAM_DOMAINS` | sreday.com, conf42.com, llmday.com, platformday.com, promptengineering.rocks, devopsnotdead.com | Threads with only these people go to **Team** |
| `SITE_REPOS` | Snout's | Repos read for lineups and sponsor domains |

## Data

- `DATA_DIR/snap/threads.json` holds one entry per thread: subject, people, dates, the rules' and Claude's read, and Mark's done, snooze and ticks. Mail bodies are never stored; the queue's excerpts live in memory only.
- `runs.json` holds the last 20 Gmail reads, and `claude.json` the routine's last visit.
- `token` is the routine's bearer token; `google.json` and `secret` are Snap's Google connection.
