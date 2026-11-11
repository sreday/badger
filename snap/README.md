# Snap by Badger

Snap finds the loose ends in mark@sreday.com's mail and lists them at **https://scan.sreday.com/snap**:

- a sponsor or host who said "we'll think about it" and went quiet,
- a host who said "let's touch base in 6 months" (it comes back on that date),
- a speaker who cancelled but whose talk is still in the event's `talks.csv`,
- a question, contract or invoice still waiting for Mark.

Threads sit on four tabs, **Sponsors** (hosts and venues included), **Speakers**, **Team** and **Others**. The list is on the left. On the right is the picked thread: the loose end, a summary, its recency (last message, who wrote last, who has the ball, follow-up date) and up to three recommended actions. **Copy for Claude** puts an action's prompt on the clipboard, with the thread's subject, id, people and summary, ready to paste into Claude Code. Snap itself never sends, drafts or changes anything in Gmail.

**Done** and **Snooze** take a thread off the list. A new message on the thread brings it back.

Snap lives inside Snout, like Snoop. It has the same binary, deploy and admin password.

## Who does what: Snout reads, Claude thinks, on Marek's subscription

No API key and no bill (Marek 2026-11-11: "these flows live inside my Claude subscription").

**Snout, every hour (free):**

1. Lists a year of threads through the Gmail API, with the Google token Snoop already holds. Gmail's Promotions, Social and Forums are left out, and so are chats, spam and drafts.
2. Reads only the headers and snippets of threads that are new or changed since the last read (Gmail's history id). It drops:
   - threads with no reply from a person: newsletters, notifications (`List-Unsubscribe`, `Precedence: bulk`, auto-replies, `noreply@`...) and Mark's own outreach that nobody answered,
   - anything older than **30 days**, unless it looks like sponsorship or hosting (keywords, or a sponsor's domain from the event pages). Those get **a full year**.
3. Gives what's left a quick read by rules ("Unanswered", sorted into tabs by keywords; a speaker's "cancel" is caught), so the page is useful straight away. It also queues each thread for Claude as a short excerpt: who, when, and only the new text of each message (quoted replies and signatures cut), the first message and the last five.
4. Checks every speaker who cancelled against the upcoming events' `talks.csv` in the site repos. Once the talk is off the lineup, the loose end closes by itself.

**Claude, every 6 hours (the routine "Snap", Claude Code on Marek's subscription, latest Opus):**

1. `GET https://scan.sreday.com/snap/api/queue` with the bearer token. That returns up to 25 threads, plus the instructions (they live in `prompt.go`, so changing them ships with Snout, not by editing the routine) and how many remain.
2. Reads them and `POST`s `/snap/api/answers`: `{"model": ..., "threads": [{id, history, category, loose_end, label, why, summary, waiting_on, follow_up_on, priority, company, event, actions, speaker_cancelled, speaker_cancelled_event}]}`.
3. Repeats until the queue is empty, or 8 rounds. A thread Claude has read is not queued again until it changes, so a quiet inbox costs nothing. An answer for a thread that got new mail in the meantime is dropped, and the thread is queued again.

The Claude menu in the header shows the queue, the last answer and the routine's link (**Run now** is on the routine's page).

Why not the Gmail connector inside the routine? Tested 2026-11-11: the routine's safety check flagged posting mail from the connector to an outside server as exfiltration. Here the routine only gets excerpts from Snap and gives answers back to Snap. That passed the check, and the connector's full-thread reads would also cost far more of the subscription.

## Setup

1. **Gmail API** on the Google Cloud project `sreday-badger-scan`. Enabled on 2026-11-11.
2. **Reconnect Google in Snoop** (`/snoop`, Google menu, Reconnect) and allow "Read your email". Snoop asks for `gmail.readonly` now, and Snap borrows Snoop's token.
3. **Network:** the routine's cloud environment must be allowed to reach `scan.sreday.com` (claude.ai/code, the environment's settings, network access: add the host). Otherwise its proxy refuses the connection ("CONNECT tunnel failed, response 403").
4. **The routine** holds the token from `/opt/badger-scan/data/snap/token` (made on Snap's first start) in its prompt. `SNAP_ROUTINE` in Snout's env is the routine's page, for the Claude menu.

| Variable | Default | |
|---|---|---|
| `SNAP_EVERY` | `1h` | How often Snout reads Gmail |
| `SNAP_ROUTINE` | claude.ai/code/routines | The Claude routine's page |
| `SNAP_TEAM_DOMAINS` | sreday.com, conf42.com, llmday.com, platformday.com, promptengineering.rocks, devopsnotdead.com | Threads with only these people go to **Team** |
| `SITE_REPOS` | Snout's | Repos read for lineups and sponsor domains |

## Data

- `DATA_DIR/snap/threads.json` holds one entry per thread: subject, people, dates, the rules' and Claude's read, and Mark's done, snooze and ticks. Mail bodies are never stored; the queue's excerpts live in memory only.
- `runs.json` holds the last 20 Gmail reads, and `claude.json` the routine's last visit.
- `token` is the routine's bearer token.
