package snap

// The recipe of the anonymized lists. This repo holds the plumbing of the
// lists (export.go, exportluma.go): reading the registrations, the private
// page, its link. HOW a list is made is not here: what Claude is told, what
// the numbers are, what is left in or out. That is the recipe, a private
// file in the server's data folder (snap/export-recipe.json, backed up with
// the rest of the data). Without it the lists are simply off.
//
// The recipe is read again whenever the file changes, so a change of method
// needs no deploy.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const recipeFile = "export-recipe.json"

// exportRecipe is the private file. Every text may use {placeholders},
// filled in by fill.
type exportRecipe struct {
	Instructions string `json:"instructions"` // for the routine that cleans the lists, with the rest of its instructions
	Ask          string `json:"ask"`          // what starts that routine
	Drafter      string `json:"drafter"`      // what the reply's drafter is told about the page
	Notes        struct {
		Ready string `json:"ready"` // on the card: the page is made
		Short string `json:"short"` // no page: under the bar
		Few   string `json:"few"`   // no page: too few people
	} `json:"notes"`
	// Marks: the names of the sets of labels the routine sends back.
	Marks struct {
		Count string `json:"count"` // titles that count towards the score
		Drop  string `json:"drop"`  // titles that may be left out
		Hide  string `json:"hide"`  // companies that are never shown
	} `json:"marks"`
	Bar    int `json:"bar"`    // the score a list needs, percent
	People int `json:"people"` // the people a list needs
	Rounds int `json:"rounds"` // how many times events are picked
	Thin   int `json:"thin"`   // under this many registrations an event gets company
	Wide   int `json:"wide"`   // events at most in a later round
	Back   int `json:"back_months"`
	Ahead  int `json:"ahead_months"`
	// Wider: what a later round adds, in order: "city", "brand".
	Wider         []string `json:"wider"`
	DropTitles    string   `json:"drop_titles"`    // a regexp: answers that are no job title
	DropCompanies string   `json:"drop_companies"` // a regexp: answers that are no company

	dropTitle, dropComp *regexp.Regexp
}

func (r *exportRecipe) back() time.Duration  { return time.Duration(r.Back) * 30 * 24 * time.Hour }
func (r *exportRecipe) ahead() time.Duration { return time.Duration(r.Ahead) * 30 * 24 * time.Hour }

// parseRecipe reads and checks a recipe.
func parseRecipe(data []byte) (*exportRecipe, error) {
	var r exportRecipe
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Instructions) == "" || r.Marks.Count == "" {
		return nil, fmt.Errorf("no instructions, or no mark to count")
	}
	if r.Rounds < 1 {
		r.Rounds = 1
	}
	if r.Wide < 1 {
		r.Wide = 1
	}
	if r.Back < 1 {
		r.Back = 12
	}
	if r.Ahead < 1 {
		r.Ahead = 12
	}
	var err error
	if r.DropTitles != "" {
		if r.dropTitle, err = regexp.Compile(r.DropTitles); err != nil {
			return nil, fmt.Errorf("drop_titles: %w", err)
		}
	}
	if r.DropCompanies != "" {
		if r.dropComp, err = regexp.Compile(r.DropCompanies); err != nil {
			return nil, fmt.Errorf("drop_companies: %w", err)
		}
	}
	return &r, nil
}

var recipes = struct {
	sync.Mutex
	byPath map[string]recipeEntry
}{byPath: map[string]recipeEntry{}}

type recipeEntry struct {
	at     time.Time // the file's time when it was read
	recipe *exportRecipe
}

// recipe returns the recipe on disk, nil when there is none or it is broken.
func (s *Server) recipe() *exportRecipe {
	path := filepath.Join(s.dir, recipeFile)
	st, err := os.Stat(path)
	recipes.Lock()
	defer recipes.Unlock()
	if err != nil {
		delete(recipes.byPath, path)
		return nil
	}
	if e, ok := recipes.byPath[path]; ok && e.at.Equal(st.ModTime()) {
		return e.recipe
	}
	var r *exportRecipe
	if data, err := os.ReadFile(path); err == nil {
		if r, err = parseRecipe(data); err != nil {
			log.Printf("Snap: the lists' recipe can't be used: %v", err)
			r = nil
		}
	}
	recipes.byPath[path] = recipeEntry{at: st.ModTime(), recipe: r}
	return r
}

// fill puts values into a recipe text: fill("for {sponsor}", "sponsor", "Acme").
func fill(text string, kv ...any) string {
	pairs := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, "{"+fmt.Sprint(kv[i])+"}", fmt.Sprint(kv[i+1]))
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// marksOf picks the recipe's marks out of one raw answer of the routine.
// done says the answer carried the mark that is counted: the last batch.
func (r *exportRecipe) marksOf(raw map[string]json.RawMessage) (marks map[string][]string, done bool) {
	marks = map[string][]string{}
	for _, name := range []string{r.Marks.Count, r.Marks.Drop, r.Marks.Hide} {
		v, ok := raw[name]
		if name == "" || !ok || string(v) == "null" {
			continue
		}
		var l []string
		json.Unmarshal(v, &l)
		marks[name] = l
		done = done || name == r.Marks.Count
	}
	return marks, done
}
