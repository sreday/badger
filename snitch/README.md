# Snitch by Badger

Who does what during the workday. Each team member's week is planned as missions on a Monday-Friday board, locked, and shipped to a page of their own (a fixed link + PIN). There they start, pause and finish each mission. Every click is logged; Claude reads the logs. Hosted by Snout at `/snitch`.

Kept dirt simple on purpose: one calendar, one shelf of missions, drag, lock, done.

## The board (`/snitch/`, Snout's admin login)

- Tabs per person, week arrows, **Schedule | Log**.
- **Timeline**: a track per day, Monday to Friday. "Weekly hours" (typed above the days, per worker) is spread evenly over the five days and sets the width of the table; a new value counts from that week on.
- **Shelf**: a Generic tab and one tab per brand (SREday, LLMday, PLATFORMday, Conf42), each in its colour; PEC has no missions of its own. A brand tab lists its events from a week ago to six months ahead, the closest one picked. Missions are offered by timing: due now, late, or later; once an event has started only its after-event missions remain, and a week after it nothing.
- **Planning | Notes**, a toggle beside the people tabs (kept per browser).
  - **Planning**: drag a mission into a day, between days, or back onto the shelf; stretch it by its edges. Hold Cmd (or Alt) while dragging to drop a copy of the mission, or of every picked one, and keep the originals where they are. The arrow on the right copies it right after itself on the same day, the arrow at the bottom to the next day, each only where there is room. Click a pill to pick it (shift-click for more, or pull a box over several).
  - Keys: ⌘/Ctrl C, X, V copy, cut and paste (paste lands where the pointer is, in any week of any worker, keeping the gaps between the missions); ⌘/Ctrl D copies right; Delete removes; ⌘/Ctrl A picks every open mission; ⌘/Ctrl Z undoes, ⇧⌘Z or Ctrl Y redoes (the last 50 changes of that week; a day that has closed since stays as it is). Right-click a pill for the same as a menu.
  - **Notes**: the week holds still, the page turns a little blue, and a note tab rolls down under every mission, with two ticks: **Day-sensitive** (the mission has to be done on that day, not another) and **Prioritize** (the worker sees it is to be done first). Both: done on that day, as the first mission of the day. In Planning a day-sensitive mission shows a pin, a prioritized one a flag.
- Conference missions carry links to the event's own pages (teasers, CFP, Luma, ...).
- There is no lock: the worker sees the week as it is planned. A day closes for good once it is past or the worker has started a mission on it.
- **Hints** under the calendar: who already has a mission, what was done before, days over or empty, due missions on nobody's week.
- **Auto-fill**: Claude drafts the open days. You still lock.
- **Log**: given vs logged per mission, the worker's finish notes, Claude's weekly summary, an Ask Claude box, the timeline in UK time.

## The worker's page (`/snitch/<token>`)

- PIN once (5 wrong tries lock the page for 10 minutes), then remembered for 30 days.
- Only the current week.
- Only today's missions can be started or finished. One mission runs at a time. Five seconds between clicks.
- Finish needs a short note on what was done; a link is optional.
- While a clock runs the page pings once a minute; more than 10 minutes of silence is logged as the page closed. A clock still running when the week ends is stopped.

## Humans (`/snitch/humans`)

Every week of 2026, newest first, per week a few small tables side by side (Time, Tags, Leads, Tickets, then the comments), each with a column per team member and one for the team, a row per number, zeros shown. Rows: hours (and per day), every tag set that week (new, and `+` changed, tinted in the humans app's colours), lead quality, tickets, and each worker's three longest comments. Only the team is shown: `humans-team.json` on the server names each worker by the start of their API key (`Keys`), with account numbers (`Accounts`) used until the key prefixes can be read. Any other account is left out until it is given to a worker there.

- Read every 6 hours with a login that can only read: `SNITCH_HUMANS_DB`, or Snore's `SNORE_HUMANS_DB` when that isn't set. The result is kept in `humans.json`.
- The key prefixes are read from the humans app's `users` (only `id` and `api_key_prefix`), once the login may read them: `GRANT SELECT (id, api_key_prefix) ON users TO <the read-only login>;`.
- **Tickets**: every booking this week (people with a LinkedIn link), free (price 0) vs paid, in the week's header; on a card, the bookings by people that account reached last before they booked (a visit alone doesn't count).
- **Leads**: everyone an account reached that week (a visit alone doesn't count), sorted by their LinkedIn headline into top, good, ok and weak, with the weak groups as red chips (hover: a few headlines). The week's header shows the team's weak share. The rules are in `lead-rules.json` on the server only; without that file there is no lead quality. The worker sees the same line for their own account on their page, once their account is picked here.
- The humans app has no clock. The hours come from the times of the clicks: a pause of more than 15 minutes ends a stretch, and the last click of each stretch counts 2 minutes. Work outside the app doesn't show.

## Missions

The catalogue is in `missions.go`: an in-person set (SREday, LLMday, PLATFORMday, PEC) from the team's social media plan, a separate Conf42 set, and the generic ones. Each has a window in days around the event, a default time, a how-to and links. Missions added on the board are kept in `missions.json`.

Events come from the websites (`events.go`): every event's `metadata.yml` in the site repos (also under `past/`) and Conf42's events list. Read every 6 hours.

## Claude

One routine on the Claude subscription, "Snitch: read the logs" (no connectors). It does the jobs in the queue:

| Job | When |
|---|---|
| Summary per person | Fridays from 18:00 UTC, and "Summarise now" |
| Lessons from last week's missions (better default times, standing notes) | Mondays from 06:00 UTC |
| Auto-fill a week | the button |
| Answer a question about a person's log | the Ask box |

The routine's prompt:

```
You do the jobs of Snitch by Badger.
1. GET https://scan.sreday.com/snitch/api/queue with the header "Authorization: Bearer <token>".
2. If "jobs" is empty, stop.
3. Follow "instructions" in the answer exactly and POST your answers as JSON to https://scan.sreday.com/snitch/api/answers with the same header.
4. If "remaining" is above 0, start again at 1.
```

`<token>` is in `DATA_DIR/snitch/token` (made on first start). Schedule the routine `30 6,18 * * 1,5` (UTC). For the buttons to start it at once, add an API trigger to the routine and paste the routine id (`trig_...`) and the trigger token in the board's Claude menu (or set `SNITCH_ROUTINE` / `SNITCH_ROUTINE_TOKEN`). Without that the jobs wait for the next scheduled run.

## Data (`DATA_DIR/snitch/`)

| File | Holds |
|---|---|
| `people.json` | the team: name, link token, PIN, time zone (`TZ`, the person's own "today"; Europe/London unless changed) |
| `weeks/<person>/<2026-W47>.json` | a week: hours, missions, clocks |
| `log/<person>.jsonl` | every event, append-only, UTC |
| `summaries/<person>/<week>.json` | Claude's summaries |
| `history/<person>/<week>.json` | the board's undo and redo steps for that week |
| `lessons.json`, `jobs.json`, `missions.json`, `events.json` | Claude's lessons, its jobs, added missions, the events cache |
| `token`, `secret`, `claude.json` | the routine's bearer token, the cookie key, the routine id + trigger token |

## Environment

| Variable | Meaning |
|---|---|
| `SNITCH_TZ` | the team's clock for the board and the logs (default Europe/London) |
| `SNITCH_ROUTINE`, `SNITCH_ROUTINE_TOKEN` | the routine and its API trigger token, instead of pasting them on the board |
| `SITE_REPOS` | the site repos (shared with Snout) |

## Later

Match the humans hours against the hours logged on the board.
