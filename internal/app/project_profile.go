package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nanaki-93/kogin/v2/internal/storage"
)

// The project profile says what a project is for, so analysis judges it
// against its own goal: who uses it, where it runs, which threats matter and
// which severity still counts as work. Without one, Kogin judges every
// project as a team project at the standard security level.

const projectProfileRelativePath = ".kogin/profile.json"

// ErrInvalidProfile rejects a profile with an unknown or malformed field.
var ErrInvalidProfile = errors.New("invalid project profile")

// Project kinds.
const (
	ProfilePersonal   = "personal"
	ProfileTeam       = "team"
	ProfileOpenSource = "open_source"
	ProfileEnterprise = "enterprise"
)

// Security levels, from the fewest threats in scope to the most.
const (
	SecurityBasic    = "basic"
	SecurityStandard = "standard"
	SecurityStrict   = "strict"
)

var (
	profileKinds      = []string{ProfilePersonal, ProfileTeam, ProfileOpenSource, ProfileEnterprise}
	profileShapes     = []string{"cli", "gui_backend", "web_service", "library", "other"}
	profilePlatforms  = []string{"macos", "linux", "windows"}
	profileSecurities = []string{SecurityBasic, SecurityStandard, SecurityStrict}
	// profileSeverities are the severities a finish line can stop at.
	profileSeverities = []string{"critical", "high", "medium", "low"}
)

const maxProfilePurposeRunes = 240

// ProjectProfile is what a project is and what "done" means for it.
type ProjectProfile struct {
	// Kind is personal, team, open_source or enterprise.
	Kind string `json:"kind"`
	// Purpose is one sentence on what the project is for.
	Purpose string `json:"purpose"`
	// Shape is cli, gui_backend (a desktop or web app with its own backend),
	// web_service, library or other.
	Shape string `json:"shape"`
	// Platforms are the operating systems it runs on; empty means all.
	Platforms []string `json:"platforms"`
	// Security is basic, standard or strict.
	Security string `json:"security"`
	// FinishLine is the lowest severity that still counts as work; issues
	// below it are listed but not counted.
	FinishLine string `json:"finish_line"`
	// AnalyzeTests has analysis review test files too; by default tests are
	// judged by running them.
	AnalyzeTests bool `json:"analyze_tests"`
	// Saved reports that the project has a saved profile (response only).
	Saved bool `json:"saved"`
}

type storedProjectProfile struct {
	SchemaVersion string         `json:"schema_version"`
	Profile       ProjectProfile `json:"profile"`
}

// DefaultProjectProfile is how Kogin judges a project without a profile.
func DefaultProjectProfile() ProjectProfile {
	return ProjectProfile{Kind: ProfileTeam, Shape: "other", Platforms: []string{}, Security: SecurityStandard, FinishLine: "medium"}
}

// ProjectProfile returns the active project's profile, or the default.
func (s *Service) ProjectProfile() (ProjectProfile, error) {
	return loadProjectProfile(s.manager.Root())
}

// SaveProjectProfile validates and stores the active project's profile.
func (s *Service) SaveProjectProfile(profile ProjectProfile) (ProjectProfile, error) {
	profile, err := normalizeProjectProfile(profile)
	if err != nil {
		return ProjectProfile{}, err
	}
	path, err := storage.MetadataPath(s.manager.Root(), projectProfileRelativePath)
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("save project profile: %w", err)
	}
	profile.Saved = false
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(storedProjectProfile{SchemaVersion: "1", Profile: profile}); err != nil {
		return ProjectProfile{}, err
	}
	if err := storage.WriteFile(path, data.Bytes(), 0o600); err != nil {
		return ProjectProfile{}, fmt.Errorf("save project profile: %w", err)
	}
	profile.Saved = true
	return profile, nil
}

func loadProjectProfile(root string) (ProjectProfile, error) {
	path, err := storage.MetadataPath(root, projectProfileRelativePath)
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("read project profile: %w", err)
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return DefaultProjectProfile(), nil
	}
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("read project profile: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var stored storedProjectProfile
	if err := decoder.Decode(&stored); err != nil || stored.SchemaVersion != "1" {
		return ProjectProfile{}, fmt.Errorf("read project profile: %s is not a valid profile; save the profile again in Settings", projectProfileRelativePath)
	}
	profile, err := normalizeProjectProfile(stored.Profile)
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("read project profile: %s is not a valid profile (%w); save the profile again in Settings", projectProfileRelativePath, err)
	}
	profile.Saved = true
	return profile, nil
}

// normalizeProjectProfile fills empty fields with the defaults of the
// profile's kind and rejects unknown values.
func normalizeProjectProfile(profile ProjectProfile) (ProjectProfile, error) {
	if profile.Kind == "" {
		profile.Kind = ProfileTeam
	}
	defaults := kindDefaults(profile.Kind)
	if profile.Shape == "" {
		profile.Shape = "other"
	}
	if profile.Security == "" {
		profile.Security = defaults.Security
	}
	if profile.FinishLine == "" {
		profile.FinishLine = defaults.FinishLine
	}
	profile.Purpose = strings.Join(strings.Fields(profile.Purpose), " ")
	switch {
	case !slices.Contains(profileKinds, profile.Kind):
		return ProjectProfile{}, fmt.Errorf("%w: kind must be one of %s", ErrInvalidProfile, strings.Join(profileKinds, ", "))
	case !slices.Contains(profileShapes, profile.Shape):
		return ProjectProfile{}, fmt.Errorf("%w: shape must be one of %s", ErrInvalidProfile, strings.Join(profileShapes, ", "))
	case !slices.Contains(profileSecurities, profile.Security):
		return ProjectProfile{}, fmt.Errorf("%w: security must be one of %s", ErrInvalidProfile, strings.Join(profileSecurities, ", "))
	case !slices.Contains(profileSeverities, profile.FinishLine):
		return ProjectProfile{}, fmt.Errorf("%w: finish_line must be one of %s", ErrInvalidProfile, strings.Join(profileSeverities, ", "))
	case utf8.RuneCountInString(profile.Purpose) > maxProfilePurposeRunes:
		return ProjectProfile{}, fmt.Errorf("%w: purpose is at most %d characters", ErrInvalidProfile, maxProfilePurposeRunes)
	}
	platforms := []string{}
	for _, platform := range profile.Platforms {
		if !slices.Contains(profilePlatforms, platform) {
			return ProjectProfile{}, fmt.Errorf("%w: platforms are macos, linux and windows", ErrInvalidProfile)
		}
		if !slices.Contains(platforms, platform) {
			platforms = append(platforms, platform)
		}
	}
	slices.Sort(platforms)
	profile.Platforms = platforms
	return profile, nil
}

// kindDefaults are the security level and finish line a kind starts with.
func kindDefaults(kind string) ProjectProfile {
	switch kind {
	case ProfilePersonal:
		return ProjectProfile{Security: SecurityBasic, FinishLine: "medium"}
	case ProfileEnterprise:
		return ProjectProfile{Security: SecurityStrict, FinishLine: "medium"}
	default:
		return ProjectProfile{Security: SecurityStandard, FinishLine: "medium"}
	}
}

// countsAsWork reports whether an issue of severity is at or above the
// profile's finish line.
func (p ProjectProfile) countsAsWork(severity string) bool {
	return severityRank(severity) <= severityRank(p.FinishLine)
}

func severityRank(severity string) int {
	if index := slices.Index(profileSeverities, strings.ToLower(severity)); index >= 0 {
		return index
	}
	return len(profileSeverities)
}

// analysisBrief is what the analysis prompts are told about the project;
// empty for the default profile, so projects without one are analyzed as
// before. Like AGENTS.md guidance, it is project input, not part of a saved
// report's identity: saving a profile keeps the reports already made, and
// the listing applies the profile to them at once (see findingScoper).
func (p ProjectProfile) analysisBrief() string {
	if !p.Saved {
		return ""
	}
	var text strings.Builder
	text.WriteString("\n\nPROJECT_PROFILE: judge this file against what the project is for.\n")
	fmt.Fprintf(&text, "- Project: %s %s", profileKindLabel(p.Kind), profileShapeLabel(p.Shape))
	if len(p.Platforms) > 0 {
		fmt.Fprintf(&text, " that runs on %s", strings.Join(platformLabels(p.Platforms), ", "))
	}
	text.WriteString(".\n")
	if p.Purpose != "" {
		fmt.Fprintf(&text, "- Purpose: %s\n", p.Purpose)
	}
	fmt.Fprintf(&text, "- Security level %s: %s\n", p.Security, securityScope(p.Security, p.Shape))
	if len(p.Platforms) > 0 {
		text.WriteString("- Report nothing that only happens on a platform it does not run on.\n")
	}
	text.WriteString("- Report what can go wrong for its users at this level. A hardening idea with no realistic way to fail them is not a risk.")
	return text.String()
}

// securityScope says which threats a security level covers.
func securityScope(level, shape string) string {
	switch level {
	case SecurityBasic:
		scope := "the user running it, their own account and machine, and config files they wrote are trusted. In scope: input from outside the user's control (files from others, network, other programs' output), losing or corrupting the user's data, and leaking secrets to other people or to logs. Out of scope: an attacker who already runs code as this user, wiping secrets from memory, swap or core dumps, and timing attacks."
		if shape == "gui_backend" {
			scope += " Its own frontend and backend on this machine trust each other: do not report missing authentication, CSRF or input checks between them."
		}
		return scope
	case SecurityStrict:
		return "everything in scope: other users and processes on the machine, every boundary between components (authenticate and validate across each), secrets in memory, audit, and defense in depth."
	default:
		scope := "in scope: input from outside the program, other local users, file permissions, and every network-facing API. Out of scope: an attacker who already runs code as the user and wiping secrets from memory."
		if shape == "gui_backend" {
			scope += " Its own frontend and backend trust each other while the backend listens only on this machine."
		}
		return scope
	}
}

func profileKindLabel(kind string) string {
	switch kind {
	case ProfilePersonal:
		return "a personal"
	case ProfileOpenSource:
		return "an open-source"
	case ProfileEnterprise:
		return "an enterprise"
	default:
		return "a team"
	}
}

func profileShapeLabel(shape string) string {
	switch shape {
	case "cli":
		return "command-line tool"
	case "gui_backend":
		return "app with its own frontend and backend"
	case "web_service":
		return "web service"
	case "library":
		return "library"
	default:
		return "project"
	}
}

func platformLabels(platforms []string) []string {
	labels := map[string]string{"macos": "macOS", "linux": "Linux", "windows": "Windows"}
	result := make([]string, 0, len(platforms))
	for _, platform := range platforms {
		result = append(result, labels[platform])
	}
	return result
}

// activeProfile is the active project's profile.
func (s *Service) activeProfile() ProjectProfile {
	if s.manager == nil {
		return DefaultProjectProfile()
	}
	return profileFor(s.manager.Root())
}

var profileCache = struct {
	sync.Mutex
	entries map[string]cachedProfile
}{entries: map[string]cachedProfile{}}

type cachedProfile struct {
	modified time.Time
	size     int64
	profile  ProjectProfile
}

// profileFor is root's profile, read again only when its file changes; the
// default when it cannot be read, so a damaged file never stops analysis
// (Settings reports it).
func profileFor(root string) ProjectProfile {
	path, err := storage.MetadataPath(root, projectProfileRelativePath)
	if err != nil {
		return DefaultProjectProfile()
	}
	info, err := os.Stat(path)
	if err != nil {
		return DefaultProjectProfile()
	}
	profileCache.Lock()
	defer profileCache.Unlock()
	if cached, ok := profileCache.entries[root]; ok && cached.modified.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.profile
	}
	profile, err := loadProjectProfile(root)
	if err != nil {
		profile = DefaultProjectProfile()
	}
	profileCache.entries[root] = cachedProfile{modified: info.ModTime(), size: info.Size(), profile: profile}
	return profile
}
