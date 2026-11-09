package sniff

import (
	"github.com/sreday/badger/sites"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// The speaker's page opens with the speaker's card as the event page draws
// it (a "shard"): the headshot the site serves, LinkedIn bubbles on its rim,
// the name and the talk. The picture and the LinkedIns come from the event's
// talks.csv, read through raw files and kept 10 minutes.

type sniffShard struct {
	Photo     string // the site's own speakers/<photo>.webp
	LinkedIn  string
	LinkedIn2 string
}

var (
	shardMu    sync.Mutex
	shardCache = map[string]shardEntry{} // event id -> its speakers by folded name
)

type shardEntry struct {
	byName map[string]sniffShard
	at     time.Time
}

// speakerShard is the session's speaker card, or false when the event page
// can't be read (the page then shows the name and title alone).
func speakerShard(ss SniffSession) (sniffShard, bool) {
	shardMu.Lock()
	e, ok := shardCache[ss.EventID]
	shardMu.Unlock()
	if !ok || time.Since(e.at) > 10*time.Minute {
		dir, err := sites.FindEventDir(sites.Repos(), ss.EventID, ss.EventName)
		if err != nil {
			return sniffShard{}, false
		}
		body, err := sites.Get(dir.Raw("_db/talks.csv"))
		if err != nil {
			return sniffShard{}, false
		}
		base := strings.TrimRight(metaValue(dir.Metadata, "base_path"), "/")
		if u, err := url.Parse(base); err == nil && strings.HasPrefix(u.Host, "www.") {
			u.Host = strings.TrimPrefix(u.Host, "www.") // www. only redirects
			base = u.String()
		}
		e = shardEntry{byName: talkShards(body, base), at: time.Now()}
		shardMu.Lock()
		shardCache[ss.EventID] = e
		shardMu.Unlock()
	}
	sh, ok := e.byName[foldName(ss.Who)]
	return sh, ok
}

// talkShards reads talks.csv's photos and LinkedIns, by folded name.
func talkShards(data []byte, base string) map[string]sniffShard {
	out := map[string]sniffShard{}
	rows := readCSV(data)
	if len(rows) == 0 {
		return out
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(row []string, k string) string {
		if i, ok := col[k]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	for _, row := range rows[1:] {
		name, photo := get(row, "name"), get(row, "photo")
		if name == "" || photo == "" {
			continue
		}
		sh := sniffShard{LinkedIn: linkedInURL(get(row, "linkedin")), LinkedIn2: linkedInURL(get(row, "linkedin2"))}
		if base != "" {
			sh.Photo = base + "/speakers/" + url.PathEscape(strings.TrimSuffix(photo, path.Ext(photo))+".webp")
		}
		if sh.LinkedIn == "" {
			sh.LinkedIn, sh.LinkedIn2 = sh.LinkedIn2, ""
		}
		out[foldName(name)] = sh
	}
	return out
}

// linkedInURL keeps only real LinkedIn links.
func linkedInURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "https" || (u.Host != "linkedin.com" && !strings.HasSuffix(u.Host, ".linkedin.com")) {
		return ""
	}
	return u.String()
}
