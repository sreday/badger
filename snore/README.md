# Snore by Badger

A board of speaker prospects, at `/snore/` on Snout (admin login).

- **Tabs**:
  - **SRE, LLM, PLATFORM**: every city of our footprint, from all the event folders in the site repos (top level and `past/`). Each tab is split into one section per city. Cities with an event coming come first, soonest first. After them come the others, most recent first.
  - **Conf42**: one pill per Conf42 event still to come. When an event passes, its pill goes.
- **Cards**: at most 30 per tab. Each card shows:
  - photo, name in bold, title and company, location
  - the conferences they spoke at, each linked to the talk's own page
  - a **Humans app status** pill
- **Photos**: Snore fetches them itself, reading only pages the card already links. On each page it takes the image labelled with the speaker's name, or the page's preview image when the page is about them. Each photo is copied into the data dir.
- **Humans app status**: the humans app is the team's record of who was contacted. Snore reads it every 6 hours with a login that can only read (`SNORE_HUMANS_DB`).
  - The pill shows the person's latest status there when their name matches **exactly** (case and spacing aside), and the name then links to their profile.
  - **None**: no one there has that name.
  - **unavailable**: the humans app hasn't been read.
- **Local only**: an in-person tab shows a speaker only under our cities within easy reach of where they publicly say they work. Snore doesn't pay for travel.
- **Off the board**: anyone on one of our lineups (the brand sites' `talks.csv` and Conf42's `_db/*.csv`). The lineups are read every 6 hours.
- **Actions**:
  - **×** marks a card as not a fit, for that tab, for good.
  - **Invited** hides the speaker everywhere for 90 days.

## Claude

Snore never calls a model. The routine "Snore: scout speakers" runs on the subscription.

- **Saturday**: from 05:00 UTC, Snore queues one job per tab.
- **Each run**:
  1. `GET /api/queue` with the bearer token from `DATA_DIR/snore/token` (3 tabs at a time, each leased for 90 minutes).
  2. Scouts public sources.
  3. `POST /api/check` to drop names already on our lineups.
  4. `POST /api/answers`.
- **Scout now** on the board queues the tab's job at once. The routine checks the queue every hour (07:00-22:00 UTC) on its own schedule, on the subscription. Snore never starts it through the API, and nothing needs a token.
- **No repeats**: a speaker already on the board (on any tab) is skipped. Their card stays as it is, and each person shows on one tab only.

The scouting instructions in `claude.go` are general. The sources and the scoring weights are a private recipe, `DATA_DIR/snore/recipe.txt`, edited on the server and never in this repo.

## Data

Everything lives in `DATA_DIR/snore/`:

- `state.json`: cards and marks
- `jobs.json`
- `footprint.json`: events and lineup names
- `photos/`: a copy of each picture
- `token`, `recipe.txt`
