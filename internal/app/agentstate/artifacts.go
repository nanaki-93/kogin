package agentstate

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"mime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxArtifactBytes = 4 << 20

var ErrArtifactInUse = errors.New("artifact is retained for an active run or change recovery")
var ErrArtifactConflict = errors.New("artifact selection changed")

type ArtifactKey struct {
	Session Key    `json:"session"`
	Name    string `json:"name"`
}

func (k ArtifactKey) validate() error {
	if k.Session.Validate() != nil || !validID(k.Name) || strings.IndexFunc(k.Name, unicode.IsControl) >= 0 || k.Name == "user:" {
		return fmt.Errorf("invalid artifact identity")
	}
	return nil
}

func (k ArtifactKey) scoped() ArtifactKey {
	if strings.HasPrefix(k.Name, "user:") {
		k.Session.ID = "user"
	}
	return k
}

func (k ArtifactKey) key() string { return k.scoped().Session.key() + "\x00" + k.Name }

// Artifact is private source-bearing content. Protected evidence is owned by a
// domain recovery record; generic artifact deletion cannot remove that evidence.
type Artifact struct {
	Key         ArtifactKey       `json:"key"`
	Version     int64             `json:"version"`
	Kind        string            `json:"kind"`
	MIME        string            `json:"mime"`
	DisplayName string            `json:"display_name,omitempty"`
	Data        []byte            `json:"data"`
	Hash        string            `json:"hash"`
	Size        int               `json:"size"`
	Created     time.Time         `json:"created"`
	Metadata    map[string]string `json:"metadata"`
	Provenance  map[string]string `json:"provenance"`
	Protected   bool              `json:"protected,omitempty"`
}

type ArtifactWrite struct {
	Key         ArtifactKey
	Version     int64 // Zero allocates a monotonically increasing version.
	Kind        string
	MIME        string
	DisplayName string
	Data        []byte
	Metadata    map[string]string
	Provenance  map[string]string
	Protected   bool
	Deduplicate bool // Host-owned evidence may reuse identical immutable content.
}

type artifactHistory struct {
	Key      ArtifactKey `json:"key"`
	Last     int64       `json:"last"`
	Versions []Artifact  `json:"versions"`
}

type artifactCatalog struct {
	Version   int                         `json:"version"`
	Workspace string                      `json:"workspace"`
	Entries   map[string]*artifactHistory `json:"entries"`
}

func artifactHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validArtifactMetadata(values map[string]string) bool {
	if len(values) > 64 {
		return false
	}
	size := 0
	for key, value := range values {
		size += len(key) + len(value)
		if !validID(key) || !utf8.ValidString(value) {
			return false
		}
	}
	return size <= 16<<10
}

func (a Artifact) validate() error {
	if a.Key.validate() != nil || a.Key != a.Key.scoped() || a.Version <= 0 || a.Version > 1<<53 || a.Created.IsZero() {
		return ErrCorrupt
	}
	if len(a.Data) > maxArtifactBytes || a.Size != len(a.Data) || a.Hash != artifactHash(a.Data) {
		return ErrCorrupt
	}
	return validateArtifactPayload(a)
}

func validateArtifactPayload(a Artifact) error {
	if a.Kind != "text" && a.Kind != "inline" || a.Kind == "text" && !utf8.Valid(a.Data) {
		return fmt.Errorf("invalid artifact content kind")
	}
	if len(a.MIME) > 256 || len(a.DisplayName) > 256 || !utf8.ValidString(a.DisplayName) || strings.IndexFunc(a.DisplayName, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid artifact media metadata")
	}
	if a.MIME != "" {
		if _, _, err := mime.ParseMediaType(a.MIME); err != nil {
			return fmt.Errorf("invalid artifact media type")
		}
	}
	if !validArtifactMetadata(a.Metadata) || !validArtifactMetadata(a.Provenance) {
		return fmt.Errorf("artifact metadata exceeds limits")
	}
	return nil
}

func (s *Store) SaveArtifact(ctx context.Context, request ArtifactWrite) (*Artifact, error) {
	if request.Key.validate() != nil || request.Version < 0 || request.Version > 1<<53 || len(request.Data) > maxArtifactBytes {
		return nil, fmt.Errorf("invalid or oversized artifact")
	}
	if request.Deduplicate && request.Version != 0 {
		return nil, fmt.Errorf("deduplicated evidence cannot specify a new version")
	}
	record := Artifact{Key: request.Key.scoped(), Version: request.Version, Kind: request.Kind, MIME: request.MIME, DisplayName: request.DisplayName, Data: slices.Clone(request.Data), Hash: artifactHash(request.Data), Size: len(request.Data), Created: time.Now().UTC(), Metadata: maps.Clone(request.Metadata), Provenance: maps.Clone(request.Provenance), Protected: request.Protected}
	if err := validateArtifactPayload(record); err != nil {
		return nil, err
	}
	var result *Artifact
	err := s.mutateArtifacts(ctx, func(catalog *artifactCatalog) (bool, error) {
		history := catalog.Entries[record.Key.key()]
		if history == nil {
			history = &artifactHistory{Key: record.Key, Versions: []Artifact{}}
			catalog.Entries[record.Key.key()] = history
		}
		var err error
		var changed bool
		result, changed, err = appendArtifact(history, record, request.Deduplicate)
		return changed, err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func appendArtifact(history *artifactHistory, record Artifact, deduplicate bool) (*Artifact, bool, error) {
	if deduplicate && len(history.Versions) > 0 {
		previous := history.Versions[len(history.Versions)-1]
		if previous.Hash == record.Hash && previous.Kind == record.Kind && previous.MIME == record.MIME && previous.DisplayName == record.DisplayName && previous.Protected == record.Protected && maps.Equal(previous.Metadata, record.Metadata) && maps.Equal(previous.Provenance, record.Provenance) {
			return &previous, false, nil
		}
	}
	if record.Version == 0 {
		record.Version = history.Last + 1
	}
	if record.Version <= history.Last || len(history.Versions) >= 128 {
		return nil, false, fmt.Errorf("artifact version is immutable or retention limit reached")
	}
	history.Last = record.Version
	history.Versions = append(history.Versions, record)
	return &record, true, nil
}

// ArtifactVersions includes user-scoped artifacts visible to the given session.
// An empty name lists all metadata; payload bytes are never returned in a list.
func (s *Store) ArtifactVersions(ctx context.Context, key ArtifactKey) ([]Artifact, error) {
	if key.Session.Validate() != nil || key.Name != "" && key.validate() != nil {
		return nil, fmt.Errorf("invalid artifact listing scope")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	catalog, err := s.loadArtifacts()
	if err != nil {
		return nil, err
	}
	result := []Artifact{}
	for _, history := range catalog.Entries {
		if !artifactVisible(history.Key, key) {
			continue
		}
		for _, record := range history.Versions {
			record.Data = nil
			result = append(result, record)
		}
	}
	slices.SortFunc(result, func(a, b Artifact) int {
		if a.Key.Name != b.Key.Name {
			return strings.Compare(a.Key.Name, b.Key.Name)
		}
		return cmp.Compare(a.Version, b.Version)
	})
	return result, nil
}

func artifactVisible(stored, requested ArtifactKey) bool {
	if requested.Name != "" && stored.Name != requested.Name {
		return false
	}
	requested.Name = stored.Name
	return stored == requested.scoped()
}

func (s *Store) ArtifactURI(record Artifact) string {
	return "kogin-artifact:" + s.workspace + "/" + artifactHash([]byte(record.Key.key())) + "/" + strconv.FormatInt(record.Version, 10)
}
