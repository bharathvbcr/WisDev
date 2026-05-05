package wisdev

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockDBProvider struct {
	mock.Mock
}

func (m *mockDBProvider) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	args := m.Called(ctx, sql, arguments)
	return args.Get(0).(pgconn.CommandTag), args.Error(1)
}

func (m *mockDBProvider) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	res := m.Called(ctx, sql, args)
	if res.Get(0) == nil {
		return nil, res.Error(1)
	}
	return res.Get(0).(pgx.Rows), res.Error(1)
}

func (m *mockDBProvider) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return m.Called(ctx, sql, args).Get(0).(pgx.Row)
}

func (m *mockDBProvider) Begin(ctx context.Context) (pgx.Tx, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(pgx.Tx), args.Error(1)
}

func (m *mockDBProvider) Ping(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockDBProvider) Close() {
	m.Called()
}

type mockRow struct {
	mock.Mock
}

func (m *mockRow) Scan(dest ...any) error {
	return m.Called(dest...).Error(0)
}

func TestRuntimeStateStore_DB(t *testing.T) {
	mdb := new(mockDBProvider)
	journal := NewRuntimeJournal(mdb)
	store := NewRuntimeStateStore(mdb, journal)

	mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
		return strings.Contains(sql, "DO $$")
	}), mock.Anything).Return(pgconn.CommandTag{}, nil)

	t.Run("savePolicyRecord_DB", func(t *testing.T) {
		// Mock ensureDBStorage (Journal)
		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "CREATE TABLE IF NOT EXISTS wisdev_runtime_journal")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil).Once()

		// Mock ensureStorage (StateStore)
		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "CREATE TABLE IF NOT EXISTS wisdev_policy_state")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil).Once()

		record := PersistedPolicyRecord{
			PolicyVersion: "v1",
			UserID:        "u1",
			State:         map[string]any{"key": "value"},
		}
		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO wisdev_policy_state")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil).Once()

		err := store.savePolicyRecord(record)
		assert.NoError(t, err)
	})

	t.Run("PersistPolicyMutation_DB", func(t *testing.T) {
		mdb := new(mockDBProvider)
		journal := NewRuntimeJournal(mdb)
		store := NewRuntimeStateStore(mdb, journal)

		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "DO $$")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil)

		// Mock ensureDBStorage
		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "CREATE TABLE IF NOT EXISTS wisdev_runtime_journal")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil).Once()

		// Mock ensureStorage
		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "CREATE TABLE IF NOT EXISTS wisdev_policy_state")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil).Once()

		// Mock Begin
		mtx := new(mockTx)
		mdb.On("Begin", mock.Anything).Return(mtx, nil).Once()
		mtx.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(pgconn.CommandTag{}, nil)
		mtx.On("Commit", mock.Anything).Return(nil).Once()
		mtx.On("Rollback", mock.Anything).Return(nil)

		record := PersistedPolicyRecord{PolicyVersion: "v1"}
		event := PersistedPolicyEvent{EventType: "type1"}
		journalEntry := RuntimeJournalEntry{Summary: "msg"}

		err := store.PersistPolicyMutation(record, event, journalEntry)
		assert.NoError(t, err)
	})

	t.Run("PersistAgentSessionMutation_DB", func(t *testing.T) {
		mdb := new(mockDBProvider)
		journal := NewRuntimeJournal(mdb)
		store := NewRuntimeStateStore(mdb, journal)

		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "DO $$")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil)

		// Mock ensureDBStorage
		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "CREATE TABLE IF NOT EXISTS wisdev_runtime_journal")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil).Once()

		// Mock ensureStorage
		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "CREATE TABLE IF NOT EXISTS wisdev_policy_state")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil).Once()

		// Mock Begin
		mtx := new(mockTx)
		mdb.On("Begin", mock.Anything).Return(mtx, nil).Once()
		mtx.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(pgconn.CommandTag{}, nil)
		mtx.On("Commit", mock.Anything).Return(nil).Once()
		mtx.On("Rollback", mock.Anything).Return(nil)

		payload := map[string]any{"foo": "bar"}
		journalEntry := RuntimeJournalEntry{Summary: "msg"}

		err := store.PersistAgentSessionMutation("s1", "u1", payload, journalEntry)
		assert.NoError(t, err)
	})

	t.Run("LoadAgentSession_DB", func(t *testing.T) {
		mdb := new(mockDBProvider)
		store := NewRuntimeStateStore(mdb, nil)

		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "DO $$")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil)

		// Mock ensureStorage
		mdb.On("Exec", mock.Anything, mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "CREATE TABLE IF NOT EXISTS wisdev_policy_state")
		}), mock.Anything).Return(pgconn.CommandTag{}, nil).Once()

		mr := new(mockRow)
		mdb.On("QueryRow", mock.Anything, mock.Anything, []any{"s1"}).Return(mr)
		mr.On("Scan", mock.Anything, mock.Anything, mock.Anything).Return(nil)

		_, err := store.LoadAgentSession("s1")
		assert.NoError(t, err)
	})
}

type mockTx struct {
	mock.Mock
}

func (m *mockTx) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	args := m.Called(ctx, sql, arguments)
	return args.Get(0).(pgconn.CommandTag), args.Error(1)
}
func (m *mockTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	res := m.Called(ctx, sql, args)
	return res.Get(0).(pgx.Rows), res.Error(1)
}
func (m *mockTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return m.Called(ctx, sql, args).Get(0).(pgx.Row)
}
func (m *mockTx) Commit(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *mockTx) Rollback(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *mockTx) Begin(ctx context.Context) (pgx.Tx, error) {
	args := m.Called(ctx)
	return args.Get(0).(pgx.Tx), args.Error(1)
}
func (m *mockTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	args := m.Called(ctx, tableName, columnNames, rowSrc)
	return args.Get(0).(int64), args.Error(1)
}
func (m *mockTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return m.Called(ctx, b).Get(0).(pgx.BatchResults)
}
func (m *mockTx) LargeObjects() pgx.LargeObjects {
	return m.Called().Get(0).(pgx.LargeObjects)
}
func (m *mockTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	args := m.Called(ctx, name, sql)
	return args.Get(0).(*pgconn.StatementDescription), args.Error(1)
}
func (m *mockTx) Conn() *pgx.Conn {
	return m.Called().Get(0).(*pgx.Conn)
}

func TestWisDevWorkflowAgent_MapToADKEvent_Extra(t *testing.T) {
	wa := &WisDevWorkflowAgent{}
	mockCtx := new(mockInvocationContext)
	mockCtx.On("InvocationID").Return("inv-1")

	t.Run("EventConfirmationNeed", func(t *testing.T) {
		event := PlanExecutionEvent{Type: EventConfirmationNeed, Message: "confirm me"}
		adkEvent := wa.mapToADKEvent(mockCtx, event)
		assert.NotNil(t, adkEvent)
		assert.Contains(t, adkEvent.Content.Parts[0].Text, "confirm me")
	})

	t.Run("EventPlanRevised", func(t *testing.T) {
		event := PlanExecutionEvent{Type: EventPlanRevised, Message: "revised"}
		adkEvent := wa.mapToADKEvent(mockCtx, event)
		assert.NotNil(t, adkEvent)
		assert.Contains(t, adkEvent.Content.Parts[0].Text, "revised")
	})

	t.Run("EventProgress", func(t *testing.T) {
		event := PlanExecutionEvent{Type: EventProgress}
		adkEvent := wa.mapToADKEvent(mockCtx, event)
		assert.Nil(t, adkEvent)
	})

	t.Run("Default", func(t *testing.T) {
		event := PlanExecutionEvent{Type: PlanExecutionEventType("unknown")}
		adkEvent := wa.mapToADKEvent(mockCtx, event)
		assert.Nil(t, adkEvent)
	})
}
