// Package knowledge indexes what the agent should know before it acts —
// skills, documentation and runbooks, reports, memory notes, scripts — and
// finds what fits a request (BM25 over titles, descriptions and text, with a
// small Persian→English vocabulary for operations work).
package knowledge

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Kinds of entries.
const (
	KindSkill  = "skill"
	KindDoc    = "doc"
	KindReport = "report"
	KindMemory = "memory"
	KindScript = "script"
)

// Entry is one thing that can be found.
type Entry struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`   // how to open it: skill name, doc path, script name, "memory"
	Title   string `json:"title"` // name or heading
	Summary string `json:"summary,omitempty"`
	Text    string `json:"-"` // searchable body
	Own     bool   `json:"-"` // written for this workspace (ranks above general guides)
}

// Hit is a search result.
type Hit struct {
	Entry
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet,omitempty"`
}

type posting struct {
	doc int
	tf  float64 // weighted term frequency (title and summary count more)
}

// Index is a BM25 index over entries.
type Index struct {
	entries  []Entry
	lens     []float64
	avg      float64
	postings map[string][]posting
}

const (
	k1           = 1.2
	b            = 0.75
	titleWeight  = 4
	summaryWeigh = 2
	ownBoost     = 1.3
	expandWeight = 0.6
)

// Build indexes entries.
func Build(entries []Entry) *Index {
	ix := &Index{entries: entries, lens: make([]float64, len(entries)), postings: map[string][]posting{}}
	total := 0.0
	for i, e := range entries {
		tf := map[string]float64{}
		n := 0.0
		add := func(text string, w float64) {
			for _, t := range Tokens(text) {
				tf[t] += w
				n += w
			}
		}
		add(e.Title, titleWeight)
		add(e.Summary, summaryWeigh)
		add(e.Text, 1)
		ix.lens[i] = n
		total += n
		for t, f := range tf {
			ix.postings[t] = append(ix.postings[t], posting{i, f})
		}
	}
	if len(entries) > 0 {
		ix.avg = total / float64(len(entries))
	}
	return ix
}

// Len is the number of entries.
func (ix *Index) Len() int { return len(ix.entries) }

// Search returns up to n entries matching q, best first (kind "" = any).
func (ix *Index) Search(q string, kind string, n int) []Hit {
	words := uniq(Tokens(q))
	if len(words) == 0 || len(ix.entries) == 0 {
		return nil
	}
	N := float64(len(ix.entries))
	scores := map[int]float64{}
	matched := map[int]float64{}
	for _, w := range words {
		// a word and its English translations (for Persian words): a
		// translation counts less when the word itself is found too
		forms := map[string]float64{w: 1}
		tw := 1.0
		if len(ix.postings[w]) > 0 {
			tw = expandWeight
		}
		for _, t := range expand([]string{w}) {
			if _, ok := forms[t]; !ok {
				forms[t] = tw
			}
		}
		best := map[int]float64{}
		for t, wt := range forms {
			ps := ix.postings[t]
			if len(ps) == 0 {
				continue
			}
			idf := math.Log(1 + (N-float64(len(ps))+0.5)/(float64(len(ps))+0.5))
			for _, p := range ps {
				if kind != "" && ix.entries[p.doc].Kind != kind {
					continue
				}
				norm := p.tf * (k1 + 1) / (p.tf + k1*(1-b+b*ix.lens[p.doc]/ix.avg))
				scores[p.doc] += wt * idf * norm
				best[p.doc] = math.Max(best[p.doc], wt)
			}
		}
		for d, wt := range best {
			matched[d] += wt
		}
	}
	terms := append(append([]string{}, words...), expand(words)...)
	hits := make([]Hit, 0, len(scores))
	for d, s := range scores {
		// entries matching more of the query's words rank higher
		s *= 0.5 + 0.5*matched[d]/float64(len(words))
		if ix.entries[d].Own {
			s *= ownBoost
		}
		hits = append(hits, Hit{Entry: ix.entries[d], Score: s})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Ref < hits[j].Ref
	})
	if len(hits) > n {
		hits = hits[:n]
	}
	for i := range hits {
		hits[i].Snippet = snippet(hits[i].Text, terms, 240)
	}
	return hits
}

// ---------------------------------------------------------------------------
// Text

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or of to in on at for from by with without is are be been was were it its this that these those
		as if then else when what which who how why can could should would will may might must do does did done not no yes
		i you we they he she my your our their me us them please want need make get set use using used into out up down
		all any some more most other such only own same so than too very just also about over under again here there
		into onto via per each every both few many much new now one two
		را و در به از که این آن با برای یک تا هم یا اگر بر هر من ما شما او آنها است هست نیست بود شد شود کن کنید کنم کند میخواهم میخوام لطفا رو یه چی چه چطور چرا کجا`) {
		stop[w] = true
	}
}

// Tokens splits text into lower-case words (letters and digits of any
// script), drops stop words and strips simple English plurals.
func Tokens(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		w := strings.ToLower(string(cur))
		cur = cur[:0]
		if len([]rune(w)) < 2 || stop[w] {
			return
		}
		out = append(out, stem(w))
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
			continue
		}
		if r == '‌' { // Persian zero-width non-joiner joins a word
			continue
		}
		flush()
	}
	flush()
	return out
}

// stem strips common English endings so deploy, deploys, deployed,
// deploying and deployment meet.
func stem(w string) string {
	if !isASCII(w) {
		return w
	}
	for _, suf := range []string{"ments", "ment", "ings", "ing", "ies", "ed", "es", "s"} {
		if len(w)-len(suf) < 4 || !strings.HasSuffix(w, suf) {
			continue
		}
		switch suf {
		case "ies":
			return w[:len(w)-3] + "y"
		case "s":
			if strings.HasSuffix(w, "ss") || strings.HasSuffix(w, "us") || strings.HasSuffix(w, "is") {
				return w
			}
		case "es":
			// boxes, patches; but not services, images (-e words keep e below)
			if !strings.HasSuffix(w, "xes") && !strings.HasSuffix(w, "ches") && !strings.HasSuffix(w, "shes") && !strings.HasSuffix(w, "sses") {
				continue
			}
		}
		w = w[:len(w)-len(suf)]
		break
	}
	return strings.TrimSuffix(w, "e")
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// snippet is the part of text around the first query word, on one line.
func snippet(text string, terms []string, max int) string {
	low := strings.ToLower(text)
	pos := -1
	for _, t := range terms {
		if i := strings.Index(low, t); i >= 0 && (pos < 0 || i < pos) {
			pos = i
		}
	}
	if pos < 0 {
		pos = 0
	}
	start := pos - max/3
	if start < 0 {
		start = 0
	}
	for start > 0 && !isBoundary(text, start) {
		start--
	}
	end := start + max
	if end > len(text) {
		end = len(text)
	}
	for end < len(text) && !isBoundary(text, end) {
		end++
	}
	s := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

func isBoundary(s string, i int) bool { return i <= 0 || i >= len(s) || s[i] < 0x80 || s[i] >= 0xC0 }
