package textsearch

import (
	"regexp"
	"slices"
	"strings"
)

const (
	RRFK          = 60
	MaxTerms      = 12
	SnippetTokens = 24
	MarkStart     = "\x02"
	MarkEnd       = "\x03"
)

var (
	tokenRe = regexp.MustCompile(`"([^"]*)"|([^\W_]+)`)
	wordRe  = regexp.MustCompile(`[^\W_]+`)
)

func FTSQuery(text string) (string, error) { return FTSQueryTerms(text, MaxTerms) }

func FTSQueryTerms(text string, maxTerms int) (string, error) {
	var terms []string
	for _, loc := range tokenRe.FindAllStringSubmatchIndex(text, -1) {
		if loc[4] >= 0 && text[loc[4]:loc[5]] != "" {
			terms = append(terms, `"`+text[loc[4]:loc[5]]+`"*`)
			continue
		}
		phrase := ""
		if loc[2] >= 0 {
			phrase = text[loc[2]:loc[3]]
		}
		words := wordRe.FindAllString(phrase, -1)
		if len(words) > 0 {
			terms = append(terms, `"`+strings.Join(words, " ")+`"`)
		}
	}
	return strings.Join(terms[:min(len(terms), maxTerms)], " "), nil
}

type Scored struct {
	ID    int64
	Score float64
}

func RRF(rankings ...[]int64) []Scored { return RRFWith(RRFK, rankings...) }

func RRFWith(k int, rankings ...[]int64) []Scored {
	scores := map[int64]float64{}
	best := map[int64]int{}
	var order []int64
	for _, ranking := range rankings {
		for i, item := range ranking {
			rank := i + 1
			if _, seen := scores[item]; !seen {
				order = append(order, item)
				best[item] = rank
			}
			scores[item] = scores[item] + 1.0/float64(k+rank)
			best[item] = min(best[item], rank)
		}
	}
	out := make([]Scored, len(order))
	for i, id := range order {
		out[i] = Scored{id, scores[id]}
	}
	slices.SortFunc(out, func(a, b Scored) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if best[a.ID] != best[b.ID] {
			return best[a.ID] - best[b.ID]
		}
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out
}

type Part struct {
	Text        string
	Highlighted bool
}

func SnippetParts(marked string) []Part {
	parts := []Part{}
	for _, chunk := range strings.Split(marked, MarkStart) {
		if highlighted, plain, ok := strings.Cut(chunk, MarkEnd); ok {
			parts = append(parts, Part{highlighted, true})
			if plain != "" {
				parts = append(parts, Part{plain, false})
			}
		} else if chunk != "" {
			parts = append(parts, Part{chunk, false})
		}
	}
	return parts
}
