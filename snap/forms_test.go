package snap

import (
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestFormLeadIsInbound(t *testing.T) {
	setMine("mark@sreday.com", []string{"sreday.com", "llmday.com", "conf42.com"})
	now := time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC)
	ack := "Hey Olga, Thank you for submitting the form, here's what we're working with: Olga from Spacelift would like to learn more about Sponsoring. Mark will reply and share the relevant prospectuses soon."
	form := Mail{FromMe: true, Form: true, At: now.Add(-10 * time.Minute), Snippet: ack, Text: ack}
	form.From = mail.Address{Name: "Mark", Address: "mark@sreday.com"}
	form.To = []mail.Address{{Name: "Olga Nowak", Address: "olga@spacelift.io"}, {Address: "mark@sreday.com"}}
	p := &ParsedThread{ID: "f", Subject: "Spacelift <> SREday, PLATFORMday", Mails: []Mail{form}}
	team := []string{"sreday.com", "llmday.com", "conf42.com"}
	m := Look(p, now, team, nil)
	if m.Skip != "" || m.Waiting != "you" || m.LastFrom != "Olga Nowak" || !m.Sponsor || m.Internal {
		t.Fatalf("a form lead should read as the lead writing in: %+v", m)
	}
	if len(m.People) != 1 || m.People[0].Email != "olga@spacelift.io" {
		t.Fatalf("people: %+v", m.People)
	}
	th := &Thread{}
	applyMeta(th, p, m)
	if !th.FreshInbound(now) || !th.Ripe(now) {
		t.Fatal("a fresh form lead should go to Claude through the fast lane")
	}
	if a := Rules(p, m); a.Category != "sponsors" || !a.Loose {
		t.Fatalf("rules: %+v", a)
	}
	if ex := Excerpt(p, m); !strings.Contains(ex, "WEBSITE FORM") || strings.Contains(ex, "] Mark:") {
		t.Fatalf("the excerpt should say it's the form, not Mark: %s", ex)
	}
	if !strings.Contains(Instructions, "WEBSITE FORM LEADS") {
		t.Error("the instructions don't cover form leads")
	}
	// Once Mark really answers, the ball is with them.
	reply := mailFrom("Olga Nowak", "olga@spacelift.io", now.Add(-time.Minute), true, "Hey Olga, here's the prospectus.")
	p.Mails = append(p.Mails, reply)
	if m := Look(p, now, team, nil); m.Waiting != "them" || m.LastFrom != "me" {
		t.Fatalf("after Mark's own reply: %+v", m)
	}
	// Plain outreach nobody answered is still skipped.
	cold := &ParsedThread{ID: "c", Subject: "Sponsor SREday?", Mails: []Mail{mailFrom("Kim", "kim@y.com", now, true, "Hey Kim")}}
	if m := Look(cold, now, team, nil); m.Skip != "no reply from a person" {
		t.Fatalf("cold outreach: %q", m.Skip)
	}
}

func TestMarksOtherAddresses(t *testing.T) {
	setMine("mark@sreday.com", []string{"sreday.com", "llmday.com"})
	for addr, want := range map[string]bool{
		"mark@sreday.com": true, "Mark@LLMday.com": true, "mark@gmail.com": false, "anna@sreday.com": false, "markus@sreday.com": false, "": false,
	} {
		if isMine(addr) != want {
			t.Errorf("isMine(%q) = %v", addr, !want)
		}
	}
	// A message from mark@llmday.com with no SENT label is still Mark's.
	g := gThread{ID: "t", HistoryID: "1", Messages: []gMessage{{ID: "m", LabelIDs: []string{"INBOX"}, InternalDate: "1794600000000", Snippet: "Sounds good!",
		Payload: gPart{Headers: []gHeader{{"From", "Mark Pawlikowski <mark@llmday.com>"}, {"To", "seb@mindpeak.ai"}, {"Subject", "Re: Info"}}}}}}
	if p := parseThread(g); len(p.Mails) != 1 || !p.Mails[0].FromMe {
		t.Fatalf("his other address wasn't taken for him: %+v", p.Mails)
	}
	// And a lead form's answer is marked as the form.
	g.Messages[0].LabelIDs = []string{"SENT"}
	g.Messages[0].Snippet = "Hey sevval, Thank you for submitting the form, here's what we're working with"
	if p := parseThread(g); !p.Mails[0].Form {
		t.Fatal("the form's answer wasn't recognised")
	}
}
