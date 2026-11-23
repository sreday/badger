package snore

import (
	"sort"
	"strconv"
	"strings"
)

// A guessed address: when we know someone's name and company but not their
// address, we build it the way that company's addresses we do hold are
// built. With no known address at the company there is no guess.

// mailShape: how one company builds its addresses.
type mailShape struct {
	Shape string // "first.last", "flast", ...
	N     int    // how many known addresses are built this way
}

// The shapes we recognise, the common ones first (a tie goes to the earlier).
var shapeOrder = []string{"first.last", "first", "flast", "firstlast", "f.last", "firstl", "first_last", "first-last", "last", "last.first", "lastf"}

// nameParts: first and last name as they would stand in an address, "" when
// the name has fewer than two parts.
func nameParts(name string) (first, last string) {
	w := strings.Fields(normName(name))
	if len(w) < 2 {
		return "", ""
	}
	return w[0], w[len(w)-1]
}

func shapeLocal(shape, first, last string) string {
	switch shape {
	case "first.last":
		return first + "." + last
	case "first":
		return first
	case "flast":
		return first[:1] + last
	case "firstlast":
		return first + last
	case "f.last":
		return first[:1] + "." + last
	case "firstl":
		return first + last[:1]
	case "first_last":
		return first + "_" + last
	case "first-last":
		return first + "-" + last
	case "last":
		return last
	case "last.first":
		return last + "." + first
	case "lastf":
		return last + first[:1]
	}
	return ""
}

// shapeOf: which shape an address has for its owner's name, "" when none.
func shapeOf(name, email string) string {
	first, last := nameParts(name)
	i := strings.LastIndexByte(email, '@')
	if first == "" || i < 1 {
		return ""
	}
	local := strings.ToLower(email[:i])
	for _, sh := range shapeOrder {
		if shapeLocal(sh, first, last) == local {
			return sh
		}
	}
	return ""
}

// namedMail is one address with its owner's name.
type namedMail struct{ Name, Email string }

// learnShapes works out each company's shape from the addresses we hold.
func learnShapes(known []namedMail) map[string]mailShape {
	count := map[string]map[string]int{}
	seen := map[string]bool{}
	for _, k := range known {
		e := cleanEmail(k.Email)
		d := workDomain(e)
		if d == "" || seen[e] || !strings.HasSuffix(e, "@"+d) {
			continue
		}
		seen[e] = true
		if sh := shapeOf(k.Name, e); sh != "" {
			if count[d] == nil {
				count[d] = map[string]int{}
			}
			count[d][sh]++
		}
	}
	rank := map[string]int{}
	for i, sh := range shapeOrder {
		rank[sh] = i
	}
	out := map[string]mailShape{}
	for d, c := range count {
		var shapes []string
		for sh := range c {
			shapes = append(shapes, sh)
		}
		sort.Slice(shapes, func(i, j int) bool {
			if c[shapes[i]] != c[shapes[j]] {
				return c[shapes[i]] > c[shapes[j]]
			}
			return rank[shapes[i]] < rank[shapes[j]]
		})
		out[d] = mailShape{Shape: shapes[0], N: c[shapes[0]]}
	}
	return out
}

// guessMail: the address a person at the company most likely has, with what
// the guess rests on. "" when we know no address at that company.
func guessMail(shapes map[string]mailShape, domain, name string) (email, basis string) {
	sh, ok := shapes[domain]
	first, last := nameParts(name)
	if !ok || first == "" || strings.HasPrefix(domain, "name:") {
		return "", ""
	}
	local := shapeLocal(sh.Shape, first, last)
	if local == "" || cleanEmail(local+"@"+domain) == "" {
		return "", ""
	}
	basis = sh.Shape + ", from 1 address we hold"
	if sh.N > 1 {
		basis = sh.Shape + ", from " + strconv.Itoa(sh.N) + " addresses we hold"
	}
	return local + "@" + domain, basis
}
