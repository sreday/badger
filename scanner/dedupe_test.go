package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDedupeGuests(t *testing.T) {
	li := func(s string) string { return "https://www.linkedin.com/in/" + s }
	guests := []Guest{
		// Work and personal email, same LinkedIn: the work one wins.
		{ID: "1", FirstName: "Alex", LastName: "Morgan", Email: "alexmorgan77@gmail.com", Title: "Platform Lead", Company: "Acme", LinkedIn: li("alex-morgan-1a2b3c")},
		{ID: "2", FirstName: "Alex", LastName: "Morgan", Email: "alex.morgan@acme.example", Title: "Platform Lead", Company: "Acme", LinkedIn: li("alex-morgan-1a2b3c")},
		// An initial and a private relay vs the full entry.
		{ID: "3", FirstName: "J", LastName: "Novak", Email: "x@privaterelay.appleid.com", Title: "Account Exec", Company: "Initech", LinkedIn: li("janovak")},
		{ID: "4", FirstName: "Jana", LastName: "Novak", Email: "jana.novak@initech.example", Title: "Regional Lead", Company: "Initech", LinkedIn: li("janovak")},
		// Same company, one without a LinkedIn.
		{ID: "5", FirstName: "Sam", LastName: "Reyes", Email: "sammy99@hotmail.com", Title: "Engineer", Company: "Globex bank"},
		{ID: "6", FirstName: "Sam", LastName: "Reyes", Email: "sam.reyes@globex.example", Title: "Engineer", Company: "Globex Bank", LinkedIn: li("aaa")},
		// A shared LinkedIn but two people.
		{ID: "7", FirstName: "Lee", LastName: "Park", Email: "lee@outlook.com", Company: "Umbrella BV", LinkedIn: li("lee-park-998877")},
		{ID: "8", FirstName: "Kim", LastName: "Ortiz", Email: "info@umbrella.example", Company: "Umbrella BV", LinkedIn: li("lee-park-998877")},
		// A placeholder LinkedIn and a common name, nothing else in common.
		{ID: "9", FirstName: "Chris", LastName: "Smith", Email: "o1@outlook.com", Company: "School", LinkedIn: li("non")},
		{ID: "10", FirstName: "Chris", LastName: "Smith", Email: "o2@gmail.com", Company: "Bank", LinkedIn: li("non")},
	}
	groups := dedupeGuests(guests)
	var got []string
	for _, g := range groups {
		ids := []string{}
		for _, x := range g {
			ids = append(ids, x.ID)
		}
		got = append(got, strings.Join(ids, "+"))
	}
	want := "2+1 4+3 6+5 7 8 9 10"
	if strings.Join(got, " ") != want {
		t.Errorf("groups = %v, want %s", got, want)
	}

	// The CSV has one row each, with the scan from either registration.
	var b bytes.Buffer
	n, err := WriteSponsorReadyCSV(&b, guests, nil, map[string]LeadNotes{"3": {Rating: "hot", By: "Peter"}})
	if err != nil || n != 7 {
		t.Fatalf("WriteSponsorReadyCSV = %d, %v", n, err)
	}
	if !strings.Contains(b.String(), "Jana,Novak,jana.novak@initech.example,Regional Lead,Initech,https://linkedin.com/in/janovak,hot,,Peter\n") {
		t.Errorf("CSV =\n%s", b.String())
	}
}
