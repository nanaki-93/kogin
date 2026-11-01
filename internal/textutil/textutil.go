// Package textutil holds dependency-free text helpers shared across layers.
package textutil

import (
	"strings"
	"unicode/utf8"
)

// Truncate returns at most the first n bytes of s, cut at a character
// boundary so the result stays valid UTF-8 when s is.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// TruncateTail returns at most the last n bytes of s, starting at a character
// boundary so the result stays valid UTF-8 when s is.
func TruncateTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// Clip returns s when it has at most n characters, else its start cut to n
// characters with "…" at the end, after a word when one ends in the second
// half.
func Clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut := string([]rune(s)[:n-1])
	if space := strings.LastIndexByte(cut, ' '); space > len(cut)/2 {
		cut = cut[:space]
	}
	return strings.TrimSpace(cut) + "…"
}

// FirstSentence returns the first sentence of s, with white space collapsed,
// clipped to n characters (see Clip).
func FirstSentence(s string, n int) string {
	text := strings.Join(strings.Fields(s), " ")
	return Clip(text[:sentenceEnd(text)], n)
}

// sentenceEnd is where the first sentence ends: after a '.', '!' or '?'
// followed by a space or the end of the text, unless the '.' closes an
// abbreviation such as "e.g." or "U.S.".
func sentenceEnd(text string) int {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c != '.' && c != '!' && c != '?') || (i+1 < len(text) && text[i+1] != ' ') {
			continue
		}
		if c == '.' && abbreviation(text[strings.LastIndexByte(text[:i], ' ')+1:i+1]) {
			continue
		}
		return i + 1
	}
	return len(text)
}

var abbreviations = map[string]bool{"e.g.": true, "i.e.": true, "eg.": true, "ie.": true, "etc.": true, "vs.": true, "cf.": true, "approx.": true, "incl.": true}

// abbreviation reports whether a word ending in '.' is an abbreviation: a
// common one, or single letters with dots ("U.S.", "A.").
func abbreviation(word string) bool {
	word = strings.ToLower(strings.TrimLeft(word, "([\"'"))
	if abbreviations[word] {
		return true
	}
	for _, part := range strings.Split(strings.TrimSuffix(word, "."), ".") {
		if utf8.RuneCountInString(part) != 1 {
			return false
		}
	}
	return true
}
