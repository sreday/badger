# Snore by Badger

A board of speaker prospects, at `/snore/` on Snout (admin login).

- **Tabs**:
  - **SRE, LLM, PLATFORM**: every city of our footprint, from all the event folders in the site repos (top level and `past/`). Each tab is split into one section per city. Cities with an event coming come first, soonest first. After them come the others, most recent first.
  - **Conf42**: one pill per Conf42 event still to come. When an event passes, its pill goes.
- **Cards**: at most 30 per tab. Each card shows:
  - photo (or the tab's mascot when there is none), name in bold, role, company in bold, location
  - a LinkedIn button on the photo when their profile is known, and the cfp.ninja frog when they submitted a talk there (it opens that event's proposals)
  - the conferences they spoke at, as a list, each linked to the talk's own page (real conference names only, never a placeholder)
  - one hint under the picture when it fits: **nobrainer** (match 85+), **keynote-ready** (gave a keynote), **many followers** (10K+)
  - the follower estimate: LinkedIn, X, Bluesky, Mastodon, YouTube and GitHub added up
  - a **Humans app status** pill, the only status on the card
  - **LinkedIn dot** (bottom right): blue with a profile, grey without one. Claude finds profiles with one cheap search (name + company) while scouting.
  - **Gmail dot** (bottom left): opens a Gmail draft of the speaker invitation (`SNORE_GMAIL` picks the account); grey without a public email.
  - A grey dot opens a box to type the LinkedIn or the email in; Claude never writes over what was typed.
- **Order**: who makes the board is a matter of match (relevance). On the board, women come first, each group best match first. What is published wins (a stated "she/her", the pronouns a bio is written in); otherwise a common women's first name, or Claude's judgement from the name and photo. It only orders the board and is never shown on a card. Claude is asked for at least half women among comparably strong people, and the candidate list puts relevant women first.
- **Leaving the board**: anyone on one of our lineups, a speaker in the humans app, or accepted on cfp.ninja (read every 6 hours with a read-only login, `SNORE_CFP_DB`).
- **Speaker sources**: public speaker sources of several shapes (see `shapes.go`), all listed in the private recipe, never in this repo. Every page is read politely: the site's robots rules first, its crawl delay, at least a second between two requests to one host. Videos read through an API are kept 30 days unless read again; open data files only from repositories with an open license. New sources: the recipe's fixed list, conference directories matched against the recipe's rules, and Claude.
- **Photos**: Snore fetches them itself, reading only pages the card already links. On each page it takes the image labelled with the speaker's name, or the page's preview image when the page is about them. Only square pictures count as headshots: banners and slides are never kept. Each photo is copied into the data dir.
- **Humans app status**: the humans app is the team's record of who was contacted. Snore reads it every 6 hours with a login that can only read (`SNORE_HUMANS_DB`).
  - The pill shows the person's latest status there when their name matches **exactly** (case and spacing aside), and the name then links to their profile.
  - **None**: no one there has that name.
  - **unavailable**: the humans app hasn't been read.
- **Local only**: an in-person tab shows a speaker only under our cities within easy reach of where they publicly say they work. Snore doesn't pay for travel.
- **Off the board**: anyone on one of our lineups (the brand sites' `talks.csv` and Conf42's `_db/*.csv`). The lineups are read every 6 hours.

## Claude

Snore never calls a model. The routine "Snore: scout speakers" runs on the subscription.

- **Daily**: from 05:00 UTC, Snore queues a job for every tab with room (every tab on Saturdays). Conf42 Remix is never a tab.
- **Each run**:
  1. `GET /api/queue` with the bearer token from `DATA_DIR/snore/token` (3 tabs at a time, each leased for 90 minutes).
  2. Scouts public sources.
  3. `POST /api/check` to drop names already on our lineups.
  4. `POST /api/answers`.
  5. Cards nobody looked up yet come with the queue as "contacts": one cheap search each, then `POST /api/contacts`. Emails only as the person publishes them, never guessed.
- **Scout now** on the board queues the tab's job at once. The routine checks the queue every hour (07:00-22:00 UTC) on its own schedule, on the subscription. Snore never starts it through the API, and nothing needs a token.
- **No repeats**: a speaker already on the board (on any tab) is skipped. In person beats online: a speaker local to one of our cities moves from a Conf42 tab to the in-person tab. Conf42 favours speakers far from our cities. Their card stays as it is, and each person shows on one tab only.

The scouting instructions in `claude.go` are general. The sources and the scoring weights are a private recipe, `DATA_DIR/snore/recipe.txt`, edited on the server and never in this repo.

## Data

Everything lives in `DATA_DIR/snore/`:

- `state.json`: cards and marks
- `jobs.json`
- `footprint.json`: events and lineup names
- `photos/`: a copy of each picture
- `activity.json`: the activity log, at `/snore/log`. Every card with its contacts downloads as CSV from `/snore/contacts.csv`.
- `token`, `recipe.txt`
