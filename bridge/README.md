# bridge: how Snoop and Snap talk

Snoop traces Marek's calls. Snap watches his mail. They stay separate tools: each lives in its own folder and owns its own data, tokens and code, and neither imports the other. This package holds the two small interfaces they talk through. Snout, which mounts both, hands each one the other's view at start (`snap.SetCalls(snoop)`, `snoop.SetMail(snap)`). A tool keeps only a link and a short trace of what the other did.

People are matched by email address, by work domain (not gmail.com and the like, never the team's own domains) or by company name. When in doubt there is no match.

## What Snoop does with Snap (`snoop/bridge.go`)

- **Resolve in Snap**, on every Needs attention tile: Snap drafts the email that settles that step, in the thread with those people (or a new email when there is none), in Mark's learned style. The tile shows "Snap is drafting", then "Draft ready in Gmail", and ticks itself when the email goes out. The call keeps the trace.
- A missed sales call resolves itself once Mark has emailed its people; any call notes "You emailed them after this call".
- **The pre-call brief**: the call page lists Snap's open cards with the call's people, and what Snap knows about their company.
- Claude's reading of a call gets Snap's company background when Snoop has none.

## What Snap does with Snoop (`snap/bridge.go`)

- **Related call**, a pill under Quick question on every card: the next call booked with those people, else the latest one held; greyed "No related call" otherwise.
- Claude reads a thread together with the notes of the calls with the same people (summary, next steps open or done), so options build on what was agreed out loud. New call notes send an open loose end back to Claude.
- A promise Mark made on a call sends the matching thread to Claude again. Promises with no thread and no email since are listed on the Hot tab ("Promised on a call, no email yet"), each linking to its call in Snoop.
- A call booked with a card's people parks the card until the call is held. Claude may propose "offer a call" with Mark's booking link.
- Quick question gets the related calls and can search all calls (`GET /snap/api/calls?q=`): titles, notes and transcripts.
- Company background found on a call shows up in Snap's reading when Snap has none.
