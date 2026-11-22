package snore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestContactsAskFindAndInvite(t *testing.T) {
	s, h := testServer(t)
	var sre string
	for _, j := range queue(t, s, h) {
		if j["topic"] == "SRE" {
			sre = j["id"].(string)
		}
	}
	withMail := card("Mail Person", "London", 90, "London")
	withMail["email"] = "Mail.Person@Example.org"
	withMail["linkedin_url"] = "https://www.linkedin.com/in/mailperson/"
	answer(t, s, h, sre, []map[string]any{withMail, card("Plain Person", "London", 50, "London")})

	body := do(t, h, "GET", "/?t=sre", "", admin).Body.String()
	if !strings.Contains(body, "https://mail.google.com/mail/?") || !strings.Contains(body, "mail.person%40example.org") {
		t.Fatal("a card with an email should draft the invitation in Gmail")
	}
	if !strings.Contains(body, "nobrainer") || !strings.Contains(body, "≈ 5K followers") {
		t.Fatal("hint and follower estimate missing")
	}
	// Scouting looked already: no LinkedIn means the grey dot, no email the grey Gmail.
	if !strings.Contains(body, `class="badge li off"`) || !strings.Contains(body, `class="badge gm off"`) {
		t.Fatal("no grey dots for the person without contacts")
	}

	// An older card nobody looked at goes out with the queue for the cheap lookup.
	s.mu.Lock()
	for _, c := range s.st.Cards["sre"] {
		if c.Name == "Plain Person" {
			c.Looked = time.Time{}
		}
	}
	s.mu.Unlock()
	var q struct {
		Contacts []map[string]any `json:"contacts"`
	}
	json.Unmarshal(do(t, h, "GET", "/api/queue", "", bearer(s)).Body.Bytes(), &q)
	if len(q.Contacts) != 1 || q.Contacts[0]["name"] != "Plain Person" {
		t.Fatalf("contacts asked = %v", q.Contacts)
	}
	do(t, h, "POST", "/api/contacts", `{"contacts":[{"name":"Plain Person","linkedin_url":"https://www.linkedin.com/in/plain/","email":"not an email","reach":{"x":20000}}]}`, bearer(s))
	body = do(t, h, "GET", "/?t=sre", "", admin).Body.String()
	if !strings.Contains(body, `href="https://www.linkedin.com/in/plain/"`) {
		t.Fatal("the LinkedIn Claude found should show")
	}
	if !strings.Contains(body, "≈ 25K followers") {
		t.Fatal("follower counts from the contacts answer should add up")
	}

	csv := do(t, h, "GET", "/contacts.csv", "", admin).Body.String()
	if !strings.Contains(csv, "mail.person@example.org") || !strings.Contains(csv, "https://www.linkedin.com/in/plain/") {
		t.Fatalf("csv = %s", csv)
	}
	lg := do(t, h, "GET", "/log", "", admin).Body.String()
	for _, want := range []string{"Plain Person: LinkedIn found", "SRE: 2 new"} {
		if !strings.Contains(lg, want) {
			t.Fatalf("log misses %q", want)
		}
	}
}

func TestRemixIsNeverATopic(t *testing.T) {
	s, _ := testServer(t)
	s.fp.F["conf42/remix2027"] = &Folder{Brand: "conf42", Name: "Remix", Start: time.Date(2027, 2, 2, 0, 0, 0, 0, time.UTC)}
	for _, tp := range s.fp.Topics(s.now()) {
		if strings.Contains(strings.ToLower(tp.Name), "remix") {
			t.Fatal("Remix should never be listed")
		}
	}
}

func TestInPersonBeatsOnline(t *testing.T) {
	s, h := testServer(t)
	var sre, online string
	for _, j := range queue(t, s, h) {
		if j["topic"] == "SRE" {
			sre = j["id"].(string)
		} else if j["online"] == true {
			online = j["id"].(string)
		}
	}
	answer(t, s, h, online, []map[string]any{card("Mover Person", "London", 70)})
	answer(t, s, h, sre, []map[string]any{card("Mover Person", "London", 80, "London")})
	if cs := s.st.Cards["sre"]; len(cs) != 1 || cs[0].Name != "Mover Person" {
		t.Fatalf("sre = %v", cs)
	}
	for id, cs := range s.st.Cards {
		if strings.HasPrefix(id, "conf42-") && len(cs) > 0 {
			t.Fatalf("%s still has %v", id, cs[0].Name)
		}
	}
}

func TestGreyDotsTakeTypedContacts(t *testing.T) {
	s, h := testServer(t)
	var sre string
	for _, j := range queue(t, s, h) {
		if j["topic"] == "SRE" {
			sre = j["id"].(string)
		}
	}
	answer(t, s, h, sre, []map[string]any{card("Hand Typed", "London", 60, "London")})
	body := do(t, h, "GET", "/?t=sre", "", admin).Body.String()
	if !strings.Contains(body, `data-edit="linkedin" data-key="Hand Typed"`) || !strings.Contains(body, `data-edit="email" data-key="Hand Typed"`) {
		t.Fatal("grey dots should open the input dialog")
	}
	do(t, h, "POST", "/admin/setcontact", "key=Hand+Typed&topic=sre&linkedin=linkedin.com%2Fin%2Fhandtyped%3Ftrk%3Dx", admin)
	do(t, h, "POST", "/admin/setcontact", "key=Hand+Typed&topic=sre&email=Hand%40Example.org", admin)
	c := s.st.Cards["sre"][0]
	if c.LinkedIn != "https://linkedin.com/in/handtyped" || c.Email != "hand@example.org" {
		t.Fatalf("typed in: %q %q", c.LinkedIn, c.Email)
	}
	// Claude never writes over what the team typed in.
	do(t, h, "POST", "/api/contacts", `{"contacts":[{"name":"Hand Typed","linkedin_url":"https://www.linkedin.com/in/other/","email":"other@example.org"}]}`, bearer(s))
	if c.LinkedIn != "https://linkedin.com/in/handtyped" || c.Email != "hand@example.org" {
		t.Fatal("typed contacts were overwritten")
	}
}
