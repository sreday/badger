# Snore by Badger

A board of speaker prospects, at `/snore/` on Snout (admin login).

- **Tabs**: SRE, LLM and PLATFORM, each covering every city of our footprint: all event folders of the site repos, top level and `past/`. Then one tab per Conf42 topic that ran in the last two years or is coming.
- **Cards**: at most 30 per tab, best match first. Each card shows:
  - photo, name, current title and affiliation, where they are based
  - reach: LinkedIn followers, the audiences of the conferences they spoke at, X / Bluesky / Mastodon / GitHub / YouTube
  - recent talks and topics
  - a match % with its four parts (reach, topic, local, activity)
- **Local only**: in-person tabs list only speakers based within easy reach of one of our cities, judged by where they live and work. Snore doesn't pay for travel. Conf42 tabs have no location rule.
- **Off the board**: anyone on one of our lineups (the brand sites' `talks.csv` and Conf42's `_db/*.csv`). The lineups are read every 6 hours.
- **Actions**:
  - **×** marks a card not a fit, for that tab, for good.
  - **Invited** hides the speaker everywhere for 90 days.
  - Clicking the `in` chip lets you type the real LinkedIn follower count. A typed count is never overwritten by an estimate.

## Claude

Snore never calls a model. The routine "Snore: scout speakers" runs on the subscription.

- **Saturday**: from 05:00 UTC, Snore queues one job per tab.
- **Each run**:
  1. `GET /api/queue` with the bearer token from `DATA_DIR/snore/token` (3 tabs at a time, each leased for 90 minutes).
  2. Scouts the open sources.
  3. `POST /api/check` to drop names already on our lineups.
  4. `POST /api/answers`.
- **Refresh** on the board queues jobs right away and starts the routine through its API trigger. Set the routine id and token in the ⚙ menu, or with `SNORE_ROUTINE` / `SNORE_ROUTINE_TOKEN`.

The scouting instructions in `claude.go` are general. The list of sources and the scoring weights are a private recipe: `DATA_DIR/snore/recipe.txt`, edited in the ⚙ menu, never in this repo.

## Data

Everything lives in `DATA_DIR/snore/`:

- `state.json`: cards, marks, typed counts
- `jobs.json`
- `footprint.json`: events and lineup names
- `photos/`: a copy of each picture
- `token`, `claude.json`, `recipe.txt`
