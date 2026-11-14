# Snitch by Badger

Who does what during the workday. Each team member's week is planned as missions on a Monday-Friday board, locked, and shipped to a page of their own (a fixed link + PIN). There they start, pause and finish each mission. Every click is logged; Claude reads the logs. Hosted by Snout at `/snitch`.

Kept dirt simple on purpose: one calendar, one shelf of missions, drag, lock, done.

## The board (`/snitch/`, Snout's admin login)

- Tabs per person, week arrows, **Schedule | Log**.
- **Timeline**: a track per day, Monday to Friday. "Weekly hours" (typed above the days, per worker) is spread evenly over the five days and sets the width of the table; a new value counts from that week on.
- **Shelf**: a Generic tab and one tab per brand (SREday, LLMday, PLATFORMday, Conf42), each in its colour; PEC has no missions of its own. A brand tab lists its events from a week ago to six months ahead, the closest one picked. Missions are offered by timing: due now, late, or later; once an event has started only its after-event missions remain, and a week after it nothing.
- Drag a mission into a day, between days, or back onto the shelf. Hover a pill for **-30 min / +30 min**, a note, remove. Conference missions carry links to the event's own pages (teasers, CFP, Luma, ...).
- **Lock working schedule** ships the week. It can be unlocked, changed and locked again, but a day is closed for good once it is past or the worker has started a mission on it.
- **Hints** under the calendar: who already has a mission, what was done before, days over or empty, due missions on nobody's week.
- **Auto-fill**: Claude drafts the open days. You still lock.
- **Log**: given vs logged per mission, the worker's finish notes, Claude's weekly summary, an Ask Claude box, the timeline in UK time.

## The worker's page (`/snitch/<token>`)

- PIN once (5 wrong tries lock the page for 10 minutes), then remembered for 30 days.
- Only the current week, only what is locked (or already started).
- Only today's missions can be started or finished. One mission runs at a time. Five seconds between clicks.
- Finish needs a short note on what was done; a link is optional.
- While a clock runs the page pings once a minute; more than 10 minutes of silence is logged as the page closed. A clock still running when the week ends is stopped.

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
| `lessons.json`, `jobs.json`, `missions.json`, `events.json` | Claude's lessons, its jobs, added missions, the events cache |
| `token`, `secret`, `claude.json` | the routine's bearer token, the cookie key, the routine id + trigger token |

## Environment

| Variable | Meaning |
|---|---|
| `SNITCH_TZ` | the team's clock for the board and the logs (default Europe/London) |
| `SNITCH_ROUTINE`, `SNITCH_ROUTINE_TOKEN` | the routine and its API trigger token, instead of pasting them on the board |
| `SITE_REPOS` | the site repos (shared with Snout) |

## Later

The humans app: `people.json` has an `Email` per person, the key for matching outreach hours against the logged hours.
