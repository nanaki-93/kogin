package agentstate

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type State map[string]json.RawMessage
type Key struct {
	App  string `json:"app"`
	User string `json:"user"`
	ID   string `json:"id"`
}
type Event struct {
	Progress *NodeProgress   `json:"-"` // Written to the separate source-free journal.
	ID       string          `json:"id"`
	Time     time.Time       `json:"time"`
	Data     json.RawMessage `json:"data"`
}
type Session struct {
	Key     Key       `json:"key"`
	State   State     `json:"state"`
	Events  []Event   `json:"events"`
	Updated time.Time `json:"updated"`
}

func validID(value string) bool {
	return value != "" && len(value) <= 128 && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00\r\n") && utf8.ValidString(value)
}
func (k Key) Validate() error {
	for _, value := range []string{k.App, k.User, k.ID} {
		if !validID(value) {
			return fmt.Errorf("invalid agent record identity")
		}
	}
	return nil
}
func (k Key) key() string     { return k.App + "\x00" + k.User + "\x00" + k.ID }
func (k Key) userKey() string { return k.App + "\x00" + k.User }

func validateState(state State) error {
	if len(state) > 1024 {
		return fmt.Errorf("agent state key limit reached")
	}
	for key, value := range state {
		if key == "" || len(key) > 256 || !utf8.ValidString(key) || strings.ContainsRune(key, 0) || len(value) > 4<<20 || !validJSON(value) {
			return fmt.Errorf("invalid agent state value")
		}
	}
	return nil
}

func (s Session) validate() error {
	if s.Key.Validate() != nil || s.State == nil || validateState(s.State) != nil || len(s.Events) > 4096 || s.Updated.IsZero() {
		return ErrCorrupt
	}
	seen := make(map[string]bool, len(s.Events))
	for key := range s.State {
		if strings.HasPrefix(key, "app:") || strings.HasPrefix(key, "user:") || strings.HasPrefix(key, "temp:") {
			return ErrCorrupt
		}
	}
	for _, e := range s.Events {
		if !validID(e.ID) || seen[e.ID] || len(e.Data) > 8<<20 || !validJSON(e.Data) {
			return ErrCorrupt
		}
		seen[e.ID] = true
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, key Key, state State) (*Session, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if err := validateState(state); err != nil {
		return nil, err
	}
	var result *Session
	err := s.mutate(ctx, func(db *database) error {
		if _, exists := db.Sessions[key.key()]; exists {
			return fmt.Errorf("agent session already exists")
		}
		record := &Session{Key: key, State: State{}, Events: []Event{}, Updated: time.Now().UTC()}
		applyState(db, record, state)
		db.Sessions[key.key()] = record
		result = mergedSession(db, record)
		return nil
	})
	return result, err
}

func applyState(db *database, record *Session, delta State) {
	if db.AppState[record.Key.App] == nil {
		db.AppState[record.Key.App] = State{}
	}
	if db.UserState[record.Key.userKey()] == nil {
		db.UserState[record.Key.userKey()] = State{}
	}
	for key, value := range delta {
		switch {
		case strings.HasPrefix(key, "temp:"):
			continue
		case strings.HasPrefix(key, "app:"):
			db.AppState[record.Key.App][key] = slices.Clone(value)
		case strings.HasPrefix(key, "user:"):
			db.UserState[record.Key.userKey()][key] = slices.Clone(value)
		default:
			record.State[key] = slices.Clone(value)
		}
	}
}

func mergedSession(db *database, record *Session) *Session {
	copy := *record
	copy.State = maps.Clone(record.State)
	maps.Copy(copy.State, db.AppState[record.Key.App])
	maps.Copy(copy.State, db.UserState[record.Key.userKey()])
	for key, value := range copy.State {
		copy.State[key] = slices.Clone(value)
	}
	copy.Events = slices.Clone(record.Events)
	for i := range copy.Events {
		copy.Events[i].Data = slices.Clone(copy.Events[i].Data)
	}
	return &copy
}

func (s *Store) Session(ctx context.Context, key Key) (*Session, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	db, err := s.load()
	if err != nil {
		return nil, err
	}
	record, ok := db.Sessions[key.key()]
	if !ok {
		return nil, ErrNotFound
	}
	return mergedSession(db, record), nil
}

func (s *Store) Sessions(ctx context.Context, app, user string) ([]*Session, error) {
	if !validID(app) || user != "" && !validID(user) {
		return nil, fmt.Errorf("invalid session listing scope")
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	db, err := s.load()
	if err != nil {
		return nil, err
	}
	result := []*Session{}
	for _, record := range db.Sessions {
		if record.Key.App == app && (user == "" || record.Key.User == user) {
			result = append(result, mergedSession(db, record))
		}
	}
	slices.SortFunc(result, func(a, b *Session) int { return strings.Compare(a.Key.key(), b.Key.key()) })
	return result, nil
}

func (s *Store) DeleteSession(ctx context.Context, key Key) error {
	if err := key.Validate(); err != nil {
		return err
	}
	return s.mutate(ctx, func(db *database) error {
		if err := deleteSessionRuns(db, key); err != nil {
			return err
		}
		delete(db.Sessions, key.key())
		return nil
	})
}

func (s *Store) AppendEvent(ctx context.Context, key Key, event Event, delta State) (*Session, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if err := validateState(delta); err != nil {
		return nil, err
	}
	var result *Session
	err := s.mutate(ctx, func(db *database) error {
		record, ok := db.Sessions[key.key()]
		if !ok {
			return ErrNotFound
		}
		for _, previous := range record.Events {
			if previous.ID == event.ID {
				return fmt.Errorf("duplicate agent event identity")
			}
		}
		record.Events = append(record.Events, Event{ID: event.ID, Time: event.Time, Data: slices.Clone(event.Data)})
		applyState(db, record, delta)
		record.Updated = time.Now().UTC()
		result = mergedSession(db, record)
		return appendNodeProgress(db, key, event.Progress)
	})
	return result, err
}

// StateApp is the app name agent sessions and runs are stored under. It is
// "kasuri", from before the rename to Kogin, so sessions, paused runs and
// journals saved then keep working; it is a storage key, never shown.
const StateApp = "kasuri"

// ChangesApp is the app name tasks' evidence artifacts are stored under,
// kept from before the rename to Kogin for the same reason as StateApp.
const ChangesApp = "kasuri-changes"
