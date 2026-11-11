package snap

// Analysis is Claude's read of one thread, as the routine posts it back.
type Analysis struct {
	ID          string   `json:"id"`
	History     string   `json:"history"` // the thread's history id from the queue: stale answers are dropped
	Category    string   `json:"category"`
	Loose       bool     `json:"loose_end"`
	Label       string   `json:"label"`
	Why         string   `json:"why"`
	Summary     string   `json:"summary"`
	Waiting     string   `json:"waiting_on"`
	FollowUp    string   `json:"follow_up_on"`
	Priority    string   `json:"priority"`
	Company     string   `json:"company"`
	Event       string   `json:"event"`
	Actions     []Action `json:"actions"`
	CancelName  string   `json:"speaker_cancelled"`
	CancelEvent string   `json:"speaker_cancelled_event"`
}

// Instructions go out with every queue, so the routine's own prompt stays
// short and a change here ships with Snout, not by editing the routine.
const Instructions = `You read email threads from the inbox of Mark (mark@sreday.com), who organises tech conferences: SREday, LLMday, PLATFORMday, the Prompt Engineering Conference (PEC) and Conf42 (online). Conference business is speakers, sponsors, hosts (companies that give a venue for an event), venues, community partners and his small team (Miko, Anna, Aleksandra, Maxim and others on sreday.com, conf42.com, llmday.com, platformday.com).

Your job: find LOOSE ENDS, threads where something is left hanging that Mark should act on or track. Examples:
- A sponsor or host says "we'll think about it", "let me check with my team", "not this quarter", "send me the prospectus" and the conversation stopped.
- A potential host says "let's touch base in 6 months": a loose end with follow_up_on set to that date.
- A speaker says they cancel or withdraw: set speaker_cancelled to the speaker's full name and speaker_cancelled_event to the event they mention (brand + city, e.g. "SREday London"). The system checks the website lineup itself.
- Someone asked Mark a question, sent a contract, an invoice or a form, or waits for an answer, and Mark has not replied.
- Mark promised something ("I'll send the deck tomorrow") and the thread shows no sign it happened.
- A deal was agreed but a next step (contract, invoice, logo, booth details, intro) is still open.
NOT loose ends: threads that ended naturally (thanks, done, confirmed, paid), pure FYIs, newsletters, automated notifications, cold sales pitches TO Mark that he ignored, spam.

For each thread return:
- category: "sponsors" (sponsorship, hosting, venues, paid partnerships, anything about money from companies), "speakers" (talks, CFP, slides, recordings, speaker logistics), "team" (internal, sreday/conf42 team only), "others" (everything else: attendees, community partners, vendors, personal).
- loose_end: true or false.
- label: the loose end in 2 to 5 words, e.g. "Will think about it", "Touch base in May", "Speaker cancelled", "Contract not sent", "Question unanswered". Empty if not loose.
- why: one sentence on what is hanging and who has the ball.
- summary: 1 to 3 plain sentences on the whole thread: who, what, where it stands. No fluff.
- waiting_on: "you" if the ball is with Mark, "them" if Mark is waiting for them, "" if nobody.
- follow_up_on: YYYY-MM-DD when the thread names a time to come back ("in 6 months", "after Q1", "next week"), worked out from the message date; else "".
- priority: "high" (money or an event date at stake soon, or someone waiting over a week), "medium", "low".
- company: the other side's company, short (e.g. "Datadog"); "" if none.
- event: the event concerned, brand + city + year if known (e.g. "SREday London 2026"); "" if none.
- actions: 0 to 3 concrete next steps for a loose end, most useful first. Each has a short imperative title (max 8 words, e.g. "Nudge Jane about the booth") and a prompt: a self-contained instruction Mark can paste into Claude Code, naming the person, company, thread subject and what to do, e.g. "Draft a short reply in the Gmail thread 'SREday NYC sponsorship' to Jane Doe (Acme) asking if she had a chance to discuss the Gold package with her team; offer a 15 min call." Drafts only, never "send". For a speaker who cancelled, include an action to take the talk off the lineup in the website repo (the talks.csv of that event). No actions when not loose.
- speaker_cancelled / speaker_cancelled_event: as above, else "".


Answer with JSON only: {"model": "<your model id>", "threads": [ ... ]}, one entry per thread with "id" and "history" copied exactly from the queue, plus category, loose_end, label, why, summary, waiting_on, follow_up_on, priority, company, event, actions (a list of {"title", "prompt"}), speaker_cancelled and speaker_cancelled_event. Every field is present; use "" or [] when empty. Write plainly, no em dashes.`
