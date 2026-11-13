package snap

import (
	"net/mail"
	"testing"
	"time"
)

func TestBounceDeadEnds(t *testing.T) {
	now := time.Now()
	jane := mail.Address{Name: "Jane", Address: "jane@acme.com"}
	bo := mail.Address{Address: "bo@acme.com"}
	daemon := mail.Address{Address: "mailer-daemon@googlemail.com"}
	hers := Mail{From: jane, At: now.Add(-3 * time.Hour), Text: "Can you send the deck?"}
	mine := Mail{FromMe: true, To: []mail.Address{jane}, At: now.Add(-2 * time.Hour)}
	bounce := Mail{From: daemon, At: now.Add(-time.Hour), Automated: true, Bounce: true}
	thread := func(ms ...Mail) *ParsedThread { return &ParsedThread{Subject: "Deck", Mails: ms} }

	// Detection.
	if !isBounce(map[string]string{"subject": "Delivery Status Notification (Failure)"}, "mailer-daemon@googlemail.com") ||
		!isBounce(map[string]string{"content-type": `multipart/report; report-type=delivery-status; boundary="x"`}, "postmaster@outlook.com") ||
		!isBounce(map[string]string{"subject": "Undeliverable: Deck", "x-failed-recipients": "jane@acme.com"}, "x@y.com") {
		t.Fatal("missed a bounce")
	}
	if isBounce(map[string]string{"subject": "Delivery Status Notification (Delay)", "content-type": "multipart/report; report-type=delivery-status"}, "mailer-daemon@googlemail.com") ||
		isBounce(map[string]string{"subject": "Re: Deck"}, "jane@acme.com") {
		t.Fatal("a delay or a person counted as a bounce")
	}

	// Mark's reply bounced: dead end, once.
	p := thread(hers, mine, bounce)
	th := &Thread{LastAt: hers.At}
	applyMeta(th, p, Look(p, now, nil, nil))
	if !th.DeadEnd || th.Bucket(now) != "closed" || !th.BouncedAt.Equal(bounce.At) {
		t.Fatalf("bounce: %+v", th)
	}
	th.DeadEnd = false // Marek brings it back by hand: the same bounce doesn't close it again
	applyMeta(th, p, Look(p, now, nil, nil))
	if th.DeadEnd {
		t.Fatal("the same bounce acted twice")
	}

	// Someone wrote after the bounce: alive.
	later := Mail{From: jane, At: now, Text: "New address, here I am"}
	p = thread(hers, mine, bounce, later)
	if !Look(p, now, nil, nil).Bounced.IsZero() {
		t.Fatal("a reply after the bounce")
	}
	// An old bounce before Mark's latest send doesn't count.
	p = thread(hers, Mail{FromMe: true, To: []mail.Address{jane}, At: now.Add(-4 * time.Hour)}, Mail{From: daemon, At: now.Add(-3*time.Hour - time.Minute), Automated: true, Bounce: true}, hers, mine)
	if !Look(p, now, nil, nil).Bounced.IsZero() {
		t.Fatal("an earlier bounce")
	}
	// Sent to two, bounced for one: it reached Bo.
	two := Mail{FromMe: true, To: []mail.Address{jane, bo}, At: now.Add(-2 * time.Hour)}
	p = thread(hers, two, Mail{From: daemon, At: now.Add(-time.Hour), Automated: true, Bounce: true, Failed: []string{"jane@acme.com"}})
	if !Look(p, now, nil, nil).Bounced.IsZero() {
		t.Fatal("bounced for one of two")
	}
	p = thread(hers, two, Mail{From: daemon, At: now.Add(-time.Hour), Automated: true, Bounce: true, Failed: []string{"jane@acme.com", "bo@acme.com"}})
	if Look(p, now, nil, nil).Bounced.IsZero() {
		t.Fatal("bounced for both")
	}
}
