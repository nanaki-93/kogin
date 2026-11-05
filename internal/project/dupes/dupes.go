// Package dupes finds blocks of code repeated across a project's files: the
// same run of meaningful lines (indentation, blank lines, comments and
// punctuation-only lines ignored) in two or more places. It reads any
// language and runs in one pass over the files.
package dupes

import (
	"hash/fnv"
	"sort"
	"strings"
	"unicode"
)

// File is one source file to compare.
type File struct {
	Path    string
	Content string
}

// Place is one copy of a repeated block.
type Place struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Group is one block and every place it appears.
type Group struct {
	// Lines is how many meaningful lines the block has.
	Lines  int     `json:"lines"`
	Places []Place `json:"places"`
	// Preview is the block's first meaningful line.
	Preview string `json:"preview"`
}

// Report is the repetition in a set of files.
type Report struct {
	// Groups are the repeated blocks, longest first.
	Groups []Group `json:"groups"`
	// RepeatedLines counts the meaningful lines in copies after the first
	// of each block; TotalLines counts all meaningful lines.
	RepeatedLines int `json:"repeated_lines"`
	TotalLines    int `json:"total_lines"`
}

// MinLines is the shortest block reported, in meaningful lines.
const MinLines = 6

const (
	maxFileBytes       = 512 << 10
	maxPlacesPerWindow = 12
	maxGroups          = 200
)

type line struct {
	text   string
	number int
}

type position struct {
	file  int
	index int
}

// Find reports the blocks of at least MinLines meaningful lines that appear
// in more than one place.
func Find(files []File) Report {
	sequences := make([][]line, len(files))
	report := Report{}
	for i, file := range files {
		sequences[i] = meaningfulLines(file.Content)
		report.TotalLines += len(sequences[i])
	}
	windows := map[uint64][]position{}
	for f, sequence := range sequences {
		for i := 0; i+MinLines <= len(sequence); i++ {
			key := windowHash(sequence[i : i+MinLines])
			if len(windows[key]) < maxPlacesPerWindow {
				windows[key] = append(windows[key], position{f, i})
			}
		}
	}
	covered := map[[4]int]bool{}
	groups := map[string]*Group{}
	for _, places := range windows {
		for a := 0; a < len(places); a++ {
			for b := a + 1; b < len(places); b++ {
				first, second := places[a], places[b]
				if covered[[4]int{first.file, first.index, second.file, second.index}] || !sameWindow(sequences, first, second) {
					continue
				}
				length := extend(sequences, first, second)
				if first.file == second.file && second.index < first.index+length {
					continue // overlapping copies of a repetitive run
				}
				for k := 0; k < length; k++ {
					covered[[4]int{first.file, first.index + k, second.file, second.index + k}] = true
				}
				addPair(groups, files, sequences, first, second, length)
			}
		}
	}
	report.Groups = sortedGroups(groups)
	for _, group := range report.Groups {
		report.RepeatedLines += group.Lines * (len(group.Places) - 1)
	}
	if len(report.Groups) > maxGroups {
		report.Groups = report.Groups[:maxGroups]
	}
	return report
}

// meaningfulLines are a file's lines with indentation dropped, without blank,
// comment-only or punctuation-only lines; generated and large files have none.
func meaningfulLines(content string) []line {
	if len(content) > maxFileBytes || strings.Contains(content[:min(len(content), 512)], "Code generated") {
		return nil
	}
	var lines []line
	for number, raw := range strings.Split(content, "\n") {
		text := strings.Join(strings.Fields(raw), " ")
		if text == "" || isComment(text) || letters(text) < 3 {
			continue
		}
		lines = append(lines, line{text: text, number: number + 1})
	}
	return lines
}

func isComment(text string) bool {
	for _, prefix := range []string{"//", "#", "/*", "*", "--", "<!--"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func letters(text string) int {
	count := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			count++
		}
	}
	return count
}

func windowHash(window []line) uint64 {
	hash := fnv.New64a()
	for _, l := range window {
		_, _ = hash.Write([]byte(l.text))
		_, _ = hash.Write([]byte{0})
	}
	return hash.Sum64()
}

func sameWindow(sequences [][]line, a, b position) bool {
	for k := 0; k < MinLines; k++ {
		if sequences[a.file][a.index+k].text != sequences[b.file][b.index+k].text {
			return false
		}
	}
	return true
}

// extend is how many lines match from a and b on, at least MinLines.
func extend(sequences [][]line, a, b position) int {
	first, second := sequences[a.file], sequences[b.file]
	length := MinLines
	for a.index+length < len(first) && b.index+length < len(second) && first[a.index+length].text == second[b.index+length].text {
		if a.file == b.file && a.index+length >= b.index {
			break
		}
		length++
	}
	return length
}

func addPair(groups map[string]*Group, files []File, sequences [][]line, a, b position, length int) {
	block := sequences[a.file][a.index : a.index+length]
	texts := make([]string, len(block))
	for i, l := range block {
		texts[i] = l.text
	}
	key := strings.Join(texts, "\n")
	group, ok := groups[key]
	if !ok {
		group = &Group{Lines: length, Preview: block[0].text}
		groups[key] = group
	}
	for _, p := range []position{a, b} {
		place := Place{Path: files[p.file].Path, StartLine: sequences[p.file][p.index].number, EndLine: sequences[p.file][p.index+length-1].number}
		if !containsPlace(group.Places, place) {
			group.Places = append(group.Places, place)
		}
	}
}

func containsPlace(places []Place, place Place) bool {
	for _, existing := range places {
		if existing == place {
			return true
		}
	}
	return false
}

// sortedGroups drops blocks contained in a longer reported block at the
// same places, then orders them longest first.
func sortedGroups(groups map[string]*Group) []Group {
	result := make([]Group, 0, len(groups))
	for _, group := range groups {
		sort.Slice(group.Places, func(i, j int) bool {
			if group.Places[i].Path != group.Places[j].Path {
				return group.Places[i].Path < group.Places[j].Path
			}
			return group.Places[i].StartLine < group.Places[j].StartLine
		})
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Lines != result[j].Lines {
			return result[i].Lines > result[j].Lines
		}
		return result[i].Places[0].Path+result[i].Preview < result[j].Places[0].Path+result[j].Preview
	})
	kept := result[:0]
	for _, group := range result {
		if !insideKept(kept, group) {
			kept = append(kept, group)
		}
	}
	return kept
}

func insideKept(kept []Group, group Group) bool {
	for _, longer := range kept {
		inside := true
		for _, place := range group.Places {
			if !placeWithin(longer.Places, place) {
				inside = false
				break
			}
		}
		if inside {
			return true
		}
	}
	return false
}

func placeWithin(places []Place, place Place) bool {
	for _, outer := range places {
		if outer.Path == place.Path && outer.StartLine <= place.StartLine && place.EndLine <= outer.EndLine {
			return true
		}
	}
	return false
}
