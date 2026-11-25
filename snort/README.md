# Snort by Badger

Push a website's lineup, or speaker headshots, without the sheets or GitHub. At `/snort/` on Snout (admin login).

## Push

- **Tabs**: SRE, LLM, PLATFORM, Conf42 (PEC is left out for now). Each tab lists that brand's upcoming events, soonest first, then the last three past ones. The list is read from the site repos (GitHub's file server, no hourly limit) every 6 hours (**Refresh events** reads it again now).
- **Push** reads the event's tab of the brand's WWW sheet and commits it as the event's lineup file (`<event>/_db/talks.csv` on the in-person sites, `_db/<TOPIC>_<year>.csv` on Conf42's `src`). The site builds itself; the row shows the commit and whether the website is building, live or failed.
- **Nothing from a sheet is ever shown** on a Snort page: no rows, names, counts, tab names or differences. A push answers one of three things: pushed, already up to date, or why it was refused, in Snort's own words.
- **The sheets are read through the Snort feed**, an Apps Script deployed by Marek (`SNORT_FEED`, its address with the key). The feed finds the event's tab itself (in-person: "<City> <year> Q<n>"; Conf42: the lineup file's name, e.g. `DEVOPS_2027`) and cuts the email column inside Google. If an email address or a phone number is still left anywhere, it sends nothing. Snort checks again and refuses too. The sheet ids live only in the script.
- **Refused when**: the feed isn't set up, the event has no tab (or two), the sheet's columns don't match the website's file, the tab is empty, or an email address or phone number is left.
- **Byte form**: the same as the sites' files (Google's CSV export): UTF-8, CRLF between rows, LF inside a cell, no newline at the end.

## Headshots

- Drop the finished PNGs on the page. Each one is sorted by the colour behind the face:
  - 1000x1000: SREday (maroon), LLMday (pale green) or PLATFORMday (orange) → `speakers/` of that site. A PEC headshot (rainbow) is turned away for now.
  - Conf42: tagged with its topic in the name, as conf42.com/headshots makes them (`Full Name_obs.png`) → `headshots/` in `conf42/static`, as named. The tag decides, not the colour; the card names the upcoming event that uses the tag, or says it is a new tag.
- When the colour is unclear, you pick where it goes. A file whose name is already on the site replaces it (the card says so).
- **Add headshots** commits them, one commit per site.

## Set up

- `SNORT_FEED`: the Snort feed's web app address with `?key=`. The script is shipped to Marek as `Snort feed.txt`.
- `SNORT_GITHUB_TOKENS`: `sreday=<token>,conf42=<token>`. Fine-grained tokens, one per owner, with **Contents: read and write** and **Actions: read** on the site repos only (sreday, llmday, platformday; src, static). On the VM, `sudo snort-set-token <owner> <file>` puts one in.
- Both go in `/opt/badger-scan/env`. Then restart.

The server prints counts only. Tests use invented people.
