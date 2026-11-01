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

// FirstSentence returns the first sentence of s (through the first '.', '!'
// or '?' followed by white space or the end), with white space collapsed; a
// longer one is cut to at most n bytes, at a word when it can be, with "…".
func FirstSentence(s string, n int) string {
	text := strings.Join(strings.Fields(s), " ")
	for i := 0; i < len(text); i++ {
		if c := text[i]; (c == '.' || c == '!' || c == '?') && (i+1 == len(text) || text[i+1] == ' ') {
			text = text[:i+1]
			break
		}
	}
	if len(text) <= n {
		return text
	}
	cut := Truncate(text, n-len("…"))
	// Cut at a word when one ends in the second half.
	if space := strings.LastIndexByte(cut, ' '); space > len(cut)/2 {
		cut = cut[:space]
	}
	return strings.TrimSpace(cut) + "…"
}
