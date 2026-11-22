package snore

import "testing"

func TestVocabColumn(t *testing.T) {
	for _, c := range []struct {
		name, typ string
		want      bool
	}{
		{"event_type", "character varying", true},
		{"status", "text", true},
		{"Stage", "USER-DEFINED", true},
		{"name", "text", false},
		{"linked_in", "character varying", false},
		{"prototype", "text", false},
		{"event_type", "integer", false},
	} {
		if got := vocabColumn(c.name, c.typ); got != c.want {
			t.Errorf("vocabColumn(%q, %q) = %v, want %v", c.name, c.typ, got, c.want)
		}
	}
}
