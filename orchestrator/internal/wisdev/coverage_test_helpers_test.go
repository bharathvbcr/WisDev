package wisdev

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type coverageMockDBProvider struct {
	mock.Mock
}

func (m *coverageMockDBProvider) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	args := m.Called(ctx, sql, arguments)
	return args.Get(0).(pgconn.CommandTag), args.Error(1)
}

func (m *coverageMockDBProvider) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	res := m.Called(ctx, sql, args)
	if res.Get(0) == nil {
		return nil, res.Error(1)
	}
	return res.Get(0).(pgx.Rows), res.Error(1)
}

func (m *coverageMockDBProvider) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return m.Called(ctx, sql, args).Get(0).(pgx.Row)
}

func (m *coverageMockDBProvider) Begin(ctx context.Context) (pgx.Tx, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(pgx.Tx), args.Error(1)
}

func (m *coverageMockDBProvider) Ping(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *coverageMockDBProvider) Close() {
	m.Called()
}

type coverageFakeRows struct {
	values     [][]any
	errors     []error
	index      int
	closed     bool
	commandTag pgconn.CommandTag
	fieldDesc  []pgconn.FieldDescription
	rawValues  [][]byte
}

func (r *coverageFakeRows) Close() {
	r.closed = true
}

func (r *coverageFakeRows) Err() error {
	return nil
}

func (r *coverageFakeRows) CommandTag() pgconn.CommandTag {
	return r.commandTag
}

func (r *coverageFakeRows) FieldDescriptions() []pgconn.FieldDescription {
	return r.fieldDesc
}

func (r *coverageFakeRows) Next() bool {
	r.index++
	return r.index < len(r.values)
}

func (r *coverageFakeRows) Scan(dest ...any) error {
	if r.index < 0 || r.index >= len(r.values) {
		return assert.AnError
	}
	if len(r.errors) > r.index && r.errors[r.index] != nil {
		return r.errors[r.index]
	}
	if len(dest) != len(r.values[r.index]) {
		return assert.AnError
	}
	for i, value := range r.values[r.index] {
		switch pointer := dest[i].(type) {
		case *string:
			pointerValue, ok := value.(string)
			if !ok {
				return assert.AnError
			}
			*pointer = pointerValue
		case *int:
			pointerValue, ok := value.(int)
			if !ok {
				return assert.AnError
			}
			*pointer = pointerValue
		case *float64:
			pointerValue, ok := value.(float64)
			if !ok {
				return assert.AnError
			}
			*pointer = pointerValue
		default:
			return assert.AnError
		}
	}
	return nil
}

func (r *coverageFakeRows) Values() ([]any, error) {
	if r.index < 0 || r.index >= len(r.values) {
		return nil, assert.AnError
	}
	return r.values[r.index], nil
}

func (r *coverageFakeRows) RawValues() [][]byte {
	return r.rawValues
}

func (r *coverageFakeRows) Conn() *pgx.Conn {
	return nil
}

type coverageTestMemoryStore struct {
	working   map[string][]MemoryEntry
	longTerm  map[string][]MemoryEntry
	artifacts map[string][]MemoryEntry
	prefs     map[string][]MemoryEntry
}

func newCoverageTestMemoryStore() *coverageTestMemoryStore {
	return &coverageTestMemoryStore{
		working:   map[string][]MemoryEntry{},
		longTerm:  map[string][]MemoryEntry{},
		artifacts: map[string][]MemoryEntry{},
		prefs:     map[string][]MemoryEntry{},
	}
}

func (s *coverageTestMemoryStore) SaveWorkingMemory(_ context.Context, sessionID string, entries []MemoryEntry, _ time.Duration) error {
	s.working[sessionID] = append([]MemoryEntry(nil), entries...)
	return nil
}

func (s *coverageTestMemoryStore) LoadWorkingMemory(_ context.Context, sessionID string) ([]MemoryEntry, error) {
	return append([]MemoryEntry(nil), s.working[sessionID]...), nil
}

func (s *coverageTestMemoryStore) SaveLongTermVector(_ context.Context, userID string, entries []MemoryEntry, _ time.Duration) error {
	s.longTerm[userID] = append([]MemoryEntry(nil), entries...)
	return nil
}

func (s *coverageTestMemoryStore) LoadLongTermVector(_ context.Context, userID string) ([]MemoryEntry, error) {
	return append([]MemoryEntry(nil), s.longTerm[userID]...), nil
}

func (s *coverageTestMemoryStore) AppendLongTermVector(_ context.Context, userID string, entries []MemoryEntry, _ time.Duration) error {
	s.longTerm[userID] = mergeEntries(s.longTerm[userID], entries)
	return nil
}

func (s *coverageTestMemoryStore) SaveArtifacts(_ context.Context, sessionID string, entries []MemoryEntry) error {
	s.artifacts[sessionID] = append([]MemoryEntry(nil), entries...)
	return nil
}

func (s *coverageTestMemoryStore) LoadArtifacts(_ context.Context, sessionID string) ([]MemoryEntry, error) {
	return append([]MemoryEntry(nil), s.artifacts[sessionID]...), nil
}

func (s *coverageTestMemoryStore) SaveUserPreferences(_ context.Context, userID string, entries []MemoryEntry) error {
	s.prefs[userID] = append([]MemoryEntry(nil), entries...)
	return nil
}

func (s *coverageTestMemoryStore) LoadUserPreferences(_ context.Context, userID string) ([]MemoryEntry, error) {
	return append([]MemoryEntry(nil), s.prefs[userID]...), nil
}

func (s *coverageTestMemoryStore) SaveTiers(_ context.Context, sessionID, userID string, tiers *MemoryTierState, _, _ time.Duration) error {
	if tiers == nil {
		return nil
	}
	s.working[sessionID] = append([]MemoryEntry(nil), tiers.ShortTermWorking...)
	s.longTerm[userID] = append([]MemoryEntry(nil), tiers.LongTermVector...)
	s.artifacts[sessionID] = append([]MemoryEntry(nil), tiers.ArtifactMemory...)
	s.prefs[userID] = append([]MemoryEntry(nil), tiers.UserPersonalized...)
	return nil
}

func (s *coverageTestMemoryStore) LoadTiers(_ context.Context, sessionID, userID string) (*MemoryTierState, error) {
	return &MemoryTierState{
		ShortTermWorking: append([]MemoryEntry(nil), s.working[sessionID]...),
		LongTermVector:   append([]MemoryEntry(nil), s.longTerm[userID]...),
		ArtifactMemory:   append([]MemoryEntry(nil), s.artifacts[sessionID]...),
		UserPersonalized: append([]MemoryEntry(nil), s.prefs[userID]...),
	}, nil
}
