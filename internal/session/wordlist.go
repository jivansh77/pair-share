package session

import (
	_ "embed"
	"strings"
)

//go:embed adjectives.txt
var adjectivesRaw string

//go:embed animals.txt
var animalsRaw string

var adjectives []string
var animals []string

func init() {
	adjectives = parseWordlist(adjectivesRaw)
	animals = parseWordlist(animalsRaw)
}

func parseWordlist(raw string) []string {
	var words []string
	for _, line := range strings.Split(raw, "\n") {
		w := strings.TrimSpace(line)
		if w != "" {
			words = append(words, w)
		}
	}
	return words
}
