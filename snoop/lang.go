package snoop

import (
	"archive/zip"
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"sort"
	"time"
)

// The call's language. Meet writes its own transcript, and Gemini its notes,
// in a handful of languages only. A call in any other one (Polish, say)
// comes out of them as nonsense, so for such a call the words are Meet's
// live captions, which cover far more languages and which the Snoop
// extension reads in Chrome. Nobody knows ahead of a call which language it
// will be in, so every call has the menu, and it can be set afterwards.

// Language is one entry of the menu.
type Language struct{ Code, Name string }

// captionLanguages: languages Meet has live captions for but no transcript.
// Polish first (the one that comes up), the rest A to Z.
var captionLanguages = func() []Language {
	rest := []Language{
		{"ar", "Arabic"}, {"bg", "Bulgarian"}, {"zh", "Chinese"}, {"cs", "Czech"}, {"da", "Danish"},
		{"nl", "Dutch"}, {"fi", "Finnish"}, {"el", "Greek"}, {"he", "Hebrew"}, {"hi", "Hindi"},
		{"hu", "Hungarian"}, {"id", "Indonesian"}, {"lt", "Lithuanian"}, {"no", "Norwegian"}, {"ro", "Romanian"},
		{"ru", "Russian"}, {"sk", "Slovak"}, {"sv", "Swedish"}, {"th", "Thai"}, {"tr", "Turkish"},
		{"uk", "Ukrainian"}, {"vi", "Vietnamese"},
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	return append([]Language{{"pl", "Polish"}}, rest...)
}()

// languageName: "Polish" for "pl"; "" for a code that isn't on the menu.
func languageName(code string) string {
	for _, l := range captionLanguages {
		if l.Code == code {
			return l.Name
		}
	}
	return ""
}

// LangName is the call's language as the page shows it; "" when it's one
// Meet transcribes itself.
func (c *Call) LangName() string { return languageName(c.Lang) }

// handleLang: POST /admin/call/{id}/lang sets the call's language ("" =
// one Meet transcribes). The rest of a recurring series that hasn't
// happened yet follows: the same people will speak the same language.
func (s *Server) handleLang(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, ok := s.store.Get(id)
	lang := r.FormValue("lang")
	if !ok || lang != "" && languageName(lang) == "" {
		http.NotFound(w, r)
		return
	}
	set := func(c *Call) {
		if c.Lang != lang {
			c.Lang, c.DocHash = lang, "" // another source for the words: the Drive doc is written again
		}
	}
	s.store.Update(id, set)
	if c.Series != "" {
		now := s.now()
		for _, o := range s.store.All() {
			if o.ID != id && o.Series == c.Series && o.End.After(now) {
				s.store.Update(o.ID, set)
			}
		}
	}
	http.Redirect(w, r, s.cfg.Base+"/admin/call/"+id, http.StatusSeeOther)
}

//go:embed extension
var extensionFS embed.FS

// handleExtensionZip: GET /admin/extension.zip, the Chrome extension as it
// is in this build, to unzip and load in chrome://extensions.
func (s *Server) handleExtensionZip(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	err := fs.WalkDir(extensionFS, "extension", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := extensionFS.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := z.CreateHeader(&zip.FileHeader{Name: "snoop-" + path, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	})
	if err == nil {
		err = z.Close()
	}
	if err != nil {
		http.Error(w, "Couldn't pack the extension: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="snoop-extension.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}
