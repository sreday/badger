package snap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Analysis is Claude's read of one thread.
type Analysis struct {
	ID          string   `json:"id"`
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

// Usage is what a run cost.
type Usage struct {
	Requests     int
	Input        int64
	CacheRead    int64
	CacheWrite   int64
	Output       int64
	Threads      int
	RefusedOrBad int
}

func (u *Usage) add(o Usage) {
	u.Requests += o.Requests
	u.Input += o.Input
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
	u.Output += o.Output
	u.Threads += o.Threads
	u.RefusedOrBad += o.RefusedOrBad
}

// Analyzer reads a handful of thread excerpts and says what's loose.
type Analyzer interface {
	Analyze(ctx context.Context, today string, excerpts []string) ([]Analysis, Usage, error)
	Model() string
}

// Claude is the Analyzer behind ANTHROPIC_API_KEY.
type Claude struct {
	client anthropic.Client
	model  string
}

func NewClaude(apiKey, model string) *Claude {
	return &Claude{client: anthropic.NewClient(option.WithAPIKey(apiKey)), model: model}
}

func (c *Claude) Model() string { return c.model }

// systemPrompt never changes between requests, so it caches.
const systemPrompt = `You read email threads from the inbox of Mark (mark@sreday.com), who organises tech conferences: SREday, LLMday, PLATFORMday, the Prompt Engineering Conference (PEC) and Conf42 (online). Conference business is speakers, sponsors, hosts (companies that give a venue for an event), venues, community partners and his small team (Miko, Anna, Aleksandra, Maxim and others on sreday.com, conf42.com, llmday.com, platformday.com).

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

Return one entry per thread, with the thread's id exactly as given. Write plainly, no em dashes.`

var analysisSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"threads": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":                      map[string]any{"type": "string"},
					"category":                map[string]any{"type": "string", "enum": []string{"sponsors", "speakers", "team", "others"}},
					"loose_end":               map[string]any{"type": "boolean"},
					"label":                   map[string]any{"type": "string"},
					"why":                     map[string]any{"type": "string"},
					"summary":                 map[string]any{"type": "string"},
					"waiting_on":              map[string]any{"type": "string", "enum": []string{"you", "them", ""}},
					"follow_up_on":            map[string]any{"type": "string"},
					"priority":                map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
					"company":                 map[string]any{"type": "string"},
					"event":                   map[string]any{"type": "string"},
					"speaker_cancelled":       map[string]any{"type": "string"},
					"speaker_cancelled_event": map[string]any{"type": "string"},
					"actions": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"title":  map[string]any{"type": "string"},
								"prompt": map[string]any{"type": "string"},
							},
							"required":             []string{"title", "prompt"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"id", "category", "loose_end", "label", "why", "summary", "waiting_on", "follow_up_on", "priority", "company", "event", "actions", "speaker_cancelled", "speaker_cancelled_event"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"threads"},
	"additionalProperties": false,
}

func (c *Claude) Analyze(ctx context.Context, today string, excerpts []string) ([]Analysis, Usage, error) {
	u := Usage{Requests: 1, Threads: len(excerpts)}
	user := "Today is " + today + ".\n\n" + strings.Join(excerpts, "\n")
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 16000,
		System: []anthropic.TextBlockParam{{
			Text:         systemPrompt,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: analysisSchema},
		},
	})
	if err != nil {
		return nil, u, err
	}
	u.Input = resp.Usage.InputTokens
	u.CacheRead = resp.Usage.CacheReadInputTokens
	u.CacheWrite = resp.Usage.CacheCreationInputTokens
	u.Output = resp.Usage.OutputTokens
	switch resp.StopReason {
	case anthropic.StopReasonRefusal:
		u.RefusedOrBad = len(excerpts)
		return nil, u, fmt.Errorf("Claude declined these threads")
	case anthropic.StopReasonMaxTokens:
		u.RefusedOrBad = len(excerpts)
		return nil, u, fmt.Errorf("Claude's answer was cut off")
	}
	var text strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	var out struct {
		Threads []Analysis `json:"threads"`
	}
	if err := json.Unmarshal([]byte(text.String()), &out); err != nil {
		u.RefusedOrBad = len(excerpts)
		return nil, u, fmt.Errorf("reading Claude's answer: %w", err)
	}
	return out.Threads, u, nil
}

// batches groups excerpts so a request carries a few threads (the system
// prompt is paid once per request) without growing too long.
func batches(excerpts []string, maxThreads, maxChars int) [][]int {
	var out [][]int
	var cur []int
	size := 0
	for i, e := range excerpts {
		if len(cur) > 0 && (len(cur) >= maxThreads || size+len(e) > maxChars) {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, i)
		size += len(e)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// Price per million tokens (input, output), for the cost line on the page.
var prices = map[string][2]float64{
	"claude-haiku-5-5":  {0.10, 0.50},
	"claude-sonnet-5-5": {2, 10},
	"claude-opus-5-5":   {4, 20},
}

// Cost in dollars, or -1 for a model without a price here. Cache reads
// are a tenth of input; cache writes 1.25x.
func (u Usage) Cost(model string) float64 {
	p, ok := prices[model]
	if !ok {
		return -1
	}
	in := float64(u.Input) + 0.1*float64(u.CacheRead) + 1.25*float64(u.CacheWrite)
	return (in*p[0] + float64(u.Output)*p[1]) / 1e6
}
