# Snap by Badger

Snap finds the loose ends in mark@sreday.com's mail and lists them at **https://scan.sreday.com/snap**:

- a sponsor or host who said "we'll think about it" and went quiet,
- a host who said "let's touch base in 6 months" (it comes back on that date),
- a speaker who cancelled but whose talk is still in the event's `talks.csv`,
- a question, contract or invoice still waiting for Mark.

Threads sit on four tabs, **Sponsors** (hosts and venues included), **Speakers**, **Team** and **Others**. The list is on the left. On the right is the picked thread: the loose end, a summary, its recency (last message, who wrote last, who has the ball, follow-up date) and up to three recommended actions. **Copy for Claude** puts an action's prompt on the clipboard, with the thread's subject, id, people and summary, ready to paste into Claude Code. Snap itself never sends, drafts or changes anything in Gmail.

**Done** and **Snooze** take a thread off the list. A new message on the thread brings it back.

Snap lives inside Snout, like Snoop. It has the same binary, deploy and admin password.

## How it stays cheap

Every 6 hours (and on the round **Run now** button):

1. **Gmail, free.** Snap lists a year of threads through the Gmail API, using the Google token Snoop already holds. Gmail's Promotions, Social and Forums are left out, and so are chats, spam and drafts.
2. **Rules, free.** It reads only the headers and snippets of threads that are new or have changed since the last run (Gmail's history id). It drops:
   - threads with no reply from a person: newsletters, notifications (`List-Unsubscribe`, `Precedence: bulk`, auto-replies, `noreply@`...) and Mark's own outreach that nobody answered,
   - anything older than **30 days**, unless it looks like sponsorship or hosting (keywords, or a sponsor's domain from the event pages). Those get **a full year**.
3. **Claude, only for what changed.** The threads left go to Claude as short excerpts: who, when, and only the new text of each message (quoted replies and signatures are cut), with the first message and the last five. Six threads go in one request, so the instructions are paid once per request. A thread Claude has read is not read again until it changes, so a quiet inbox costs nothing.
4. **Lineups, free.** For every speaker who cancelled, Snap checks the upcoming events' `talks.csv` in the site repos on each run. Once the talk is off the lineup, the loose end closes by itself.

The model is `claude-haiku-5-5` by default (`SNAP_MODEL` to change it). The Claude menu in the header shows each run's threads, tokens and cost.

Without `ANTHROPIC_API_KEY`, Snap runs on the rules alone. Every thread where someone else wrote last is listed as "Unanswered", sorted into tabs by keywords. A speaker's mail that says "cancel" is still checked against the lineups. There are no summaries or proposed actions in this mode.

Why not a scheduled Claude routine with the Gmail connector? The routine's sandbox refuses to post mail-derived data to scan.sreday.com: it's flagged as exfiltration (tested 2026-11-11). Reading Gmail on the server is also free, and the connector's full-thread reads cost far more tokens.

## Setup

1. **Gmail API** on the Google Cloud project `sreday-badger-scan` (APIs & Services → enable Gmail API).
2. **Reconnect Google in Snoop** (`/snoop`, Google menu, Reconnect) and allow "Read your email". Snoop asks for `gmail.readonly` now, and Snap borrows Snoop's token. Until then, Snap's Claude menu says Gmail refused.
3. **Optional:** `ANTHROPIC_API_KEY=...` in `/opt/badger-scan/env`, then restart.

| Variable | Default | |
|---|---|---|
| `ANTHROPIC_API_KEY` | (rules only) | Claude API key |
| `SNAP_MODEL` | `claude-haiku-5-5` | |
| `SNAP_EVERY` | `6h` | Time between runs |
| `SNAP_MAX_PER_RUN` | `400` | Threads Claude reads per run at most; the rest wait for the next run (the first run reads the backlog) |
| `SNAP_TEAM_DOMAINS` | sreday.com, conf42.com, llmday.com, platformday.com, promptengineering.rocks, devopsnotdead.com | Threads with only these people go to **Team** |
| `SITE_REPOS` | Snout's | Repos read for lineups and sponsor domains |

## Data

`DATA_DIR/snap/threads.json` holds one entry per thread: subject, people, dates, Claude's read, and Mark's done, snooze and ticks. Mail bodies are never stored. `runs.json` holds the last 20 runs.
