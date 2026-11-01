package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsWholeCharacters(t *testing.T) {
	for _, test := range []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel"},
		{"hello", 0, ""},
		{"hello", -1, ""},
		{"héllo", 2, "h"},
		{"héllo", 3, "hé"},
		{"日本語", 4, "日"},
		{"日本語", 6, "日本"},
	} {
		got := Truncate(test.in, test.n)
		if got != test.want || !utf8.ValidString(got) {
			t.Errorf("Truncate(%q, %d) = %q, want %q", test.in, test.n, got, test.want)
		}
	}
}

func TestTruncateTailKeepsWholeCharacters(t *testing.T) {
	for _, test := range []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "llo"},
		{"hello", 0, ""},
		{"héllo", 4, "llo"},
		{"héllo", 5, "éllo"},
		{"日本語", 4, "語"},
	} {
		got := TruncateTail(test.in, test.n)
		if got != test.want || !utf8.ValidString(got) {
			t.Errorf("TruncateTail(%q, %d) = %q, want %q", test.in, test.n, got, test.want)
		}
	}
}

func TestFirstSentenceStopsAtTheFirstSentenceAndTheLimit(t *testing.T) {
	for _, test := range []struct {
		in   string
		n    int
		want string
	}{
		{"Saves items. Reads them back.", 80, "Saves items."},
		{"Saves  items\nto disk", 80, "Saves items to disk"},
		{"Uses v1.2 of the API. More.", 80, "Uses v1.2 of the API."},
		{"Does it work? Yes.", 80, "Does it work?"},
		{"Parses config files (e.g. YAML and TOML) and validates them. Then more.", 80, "Parses config files (e.g. YAML and TOML) and validates them."},
		{"Runs in the U.S. region only. More.", 80, "Runs in the U.S. region only."},
		{"Reads config.yaml. Then more.", 80, "Reads config.yaml."},
		{"A very long sentence that goes on", 12, "A very…"},
		{"", 10, ""},
	} {
		if got := FirstSentence(test.in, test.n); got != test.want {
			t.Errorf("FirstSentence(%q, %d) = %q, want %q", test.in, test.n, got, test.want)
		}
	}
}

func TestClipCountsCharactersAndMarksTheCut(t *testing.T) {
	for _, test := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"日本語のタイトルです", 10, "日本語のタイトルです"},
		{"日本語のタイトルです", 5, "日本語の…"},
		{"one two three four five", 12, "one two…"},
		{"Supercalifragilistic", 8, "Superca…"},
		{"anything", 0, ""},
	} {
		got := Clip(test.in, test.n)
		if got != test.want || !utf8.ValidString(got) || utf8.RuneCountInString(got) > max(test.n, 0) {
			t.Errorf("Clip(%q, %d) = %q, want %q", test.in, test.n, got, test.want)
		}
	}
}
