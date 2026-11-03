package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/session"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
)

type localSessionService struct{ store *agentstate.Store }

var _ session.Service = (*localSessionService)(nil)

func (s *localSessionService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("session create request is required")
	}
	id := req.SessionID
	if id == "" {
		id = platform.NewUUID(ctx)
	}
	state, err := encodeState(req.State)
	if err != nil {
		return nil, err
	}
	record, err := s.store.CreateSession(ctx, agentstate.Key{App: req.AppName, User: req.UserID, ID: id}, state)
	if err != nil {
		return nil, sessionStorageError(err)
	}
	view, err := newLocalSession(record)
	if err != nil {
		return nil, err
	}
	return &session.CreateResponse{Session: view}, nil
}

func (s *localSessionService) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	if req == nil || req.NumRecentEvents < 0 {
		return nil, fmt.Errorf("invalid session get request")
	}
	record, err := s.store.Session(ctx, agentstate.Key{App: req.AppName, User: req.UserID, ID: req.SessionID})
	if err != nil {
		return nil, sessionStorageError(err)
	}
	if req.NumRecentEvents > 0 {
		record.Events = record.Events[max(0, len(record.Events)-req.NumRecentEvents):]
	}
	if !req.After.IsZero() {
		record.Events = slices.DeleteFunc(record.Events, func(e agentstate.Event) bool { return e.Time.Before(req.After) })
	}
	view, err := newLocalSession(record)
	if err != nil {
		return nil, err
	}
	return &session.GetResponse{Session: view}, nil
}

func (s *localSessionService) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("session list request is required")
	}
	records, err := s.store.Sessions(ctx, req.AppName, req.UserID)
	if err != nil {
		return nil, sessionStorageError(err)
	}
	result := &session.ListResponse{Sessions: []session.Session{}}
	for _, record := range records {
		record.Events = nil
		view, err := newLocalSession(record)
		if err != nil {
			return nil, err
		}
		result.Sessions = append(result.Sessions, view)
	}
	return result, nil
}

func (s *localSessionService) Delete(ctx context.Context, req *session.DeleteRequest) error {
	if req == nil {
		return fmt.Errorf("session delete request is required")
	}
	return sessionStorageError(s.store.DeleteSession(ctx, agentstate.Key{App: req.AppName, User: req.UserID, ID: req.SessionID}))
}

func (s *localSessionService) AppendEvent(ctx context.Context, view session.Session, event *session.Event) error {
	if view == nil || event == nil {
		return fmt.Errorf("session and event are required")
	}
	if event.Partial {
		return nil
	}
	if event.ID == "" {
		event.ID = platform.NewUUID(ctx)
	}
	copy := *event
	if event.Actions.StateDelta != nil {
		copy.Actions.StateDelta = make(map[string]any)
		for key, value := range event.Actions.StateDelta {
			if !strings.HasPrefix(key, session.KeyPrefixTemp) {
				copy.Actions.StateDelta[key] = value
			}
		}
	}
	data, err := json.Marshal(&copy)
	if err != nil {
		return fmt.Errorf("encode session event: %w", err)
	}
	delta, err := encodeState(copy.Actions.StateDelta)
	if err != nil {
		return err
	}
	key := agentstate.Key{App: view.AppName(), User: view.UserID(), ID: view.ID()}
	local, _ := view.(*localSession)
	if local != nil {
		local.mu.Lock()
		defer local.mu.Unlock()
	}
	var progress *agentstate.NodeProgress
	if ctx.Value(invocationKey{}) != nil {
		progress = projectEvent(event, key)
	}
	record, err := s.store.AppendEvent(ctx, key, agentstate.Event{ID: event.ID, Time: event.Timestamp, Data: data, Progress: progress}, delta)
	if err != nil {
		return sessionStorageError(err)
	}
	if local != nil {
		local.record = record
	}
	notifyProgress(ctx)
	return nil
}

func sessionStorageError(err error) error {
	if err == nil {
		return nil
	}
	if isPersistenceCancellation(err) {
		return err
	}
	if errors.Is(err, agentstate.ErrNotFound) {
		return fmt.Errorf("%w: %w", session.ErrNotFound, err)
	}
	return fmt.Errorf("%w: %w", agentstate.ErrPersistence, err)
}

func isPersistenceCancellation(err error) bool {
	return !errors.Is(err, agentstate.ErrPersistence) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func encodeState(values map[string]any) (agentstate.State, error) {
	state := make(agentstate.State, len(values))
	for key, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode agent state: %w", err)
		}
		state[key] = data
	}
	return state, nil
}

type localSession struct {
	mu     sync.RWMutex
	key    agentstate.Key
	record *agentstate.Session
}

func newLocalSession(record *agentstate.Session) (*localSession, error) {
	for _, event := range record.Events {
		if _, err := decodeEvent(event); err != nil {
			return nil, err
		}
	}
	return &localSession{key: record.Key, record: record}, nil
}

func (s *localSession) ID() string           { return s.key.ID }
func (s *localSession) AppName() string      { return s.key.App }
func (s *localSession) UserID() string       { return s.key.User }
func (s *localSession) State() session.State { return localState{s} }
func (s *localSession) LastUpdateTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.record.Updated
}
func (s *localSession) Events() session.Events {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return localEvents(slices.Clone(s.record.Events))
}

type localState struct{ s *localSession }

func (st localState) Get(key string) (any, error) {
	st.s.mu.RLock()
	defer st.s.mu.RUnlock()
	data, ok := st.s.record.State[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	var value any
	err := json.Unmarshal(data, &value)
	return value, err
}

// ADK collects local state changes into event deltas. Only AppendEvent commits
// them, together with the event, so readers never see half an invocation step.
func (st localState) Set(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode agent state: %w", err)
	}
	if key == "" || len(key) > 256 || strings.ContainsRune(key, 0) || len(data) > 4<<20 {
		return fmt.Errorf("agent state value exceeds limits")
	}
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	st.s.record.State[key] = data
	return nil
}

func (st localState) All() iter.Seq2[string, any] {
	st.s.mu.RLock()
	values := make(agentstate.State, len(st.s.record.State))
	for key, value := range st.s.record.State {
		values[key] = slices.Clone(value)
	}
	st.s.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for key, data := range values {
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				panic("invalid validated agent state")
			}
			if !yield(key, value) {
				return
			}
		}
	}
}

type localEvents []agentstate.Event

func (e localEvents) Len() int { return len(e) }
func (e localEvents) At(i int) *session.Event {
	event, err := decodeEvent(e[i])
	if err != nil {
		panic("invalid validated agent event")
	}
	return event
}
func (e localEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for i := range e {
			if !yield(e.At(i)) {
				return
			}
		}
	}
}

func decodeEvent(record agentstate.Event) (*session.Event, error) {
	var event session.Event
	// Our versioned journal only contains Go-encoded events. Bypass the SDK's
	// cross-runtime decoder, which converts arbitrary tool numbers to float64.
	type storedEvent session.Event
	decoder := json.NewDecoder(bytes.NewReader(record.Data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber() // Tool argument integers must survive replay without float rounding.
	if err := decoder.Decode((*storedEvent)(&event)); err != nil {
		return nil, fmt.Errorf("%w: invalid stored SDK event", agentstate.ErrCorrupt)
	}
	if event.ID != record.ID || !event.Timestamp.Equal(record.Time) || event.Partial {
		return nil, agentstate.ErrCorrupt
	}
	return &event, nil
}
