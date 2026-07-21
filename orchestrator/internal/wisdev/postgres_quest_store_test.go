package wisdev

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOptionalQuestUserID(t *testing.T) {
	assert.Nil(t, normalizeOptionalQuestUserID(""))
	assert.Nil(t, normalizeOptionalQuestUserID("   "))
	assert.Nil(t, normalizeOptionalQuestUserID("not-a-uuid"))
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", normalizeOptionalQuestUserID("550e8400-e29b-41d4-a716-446655440000").(string))
}

func TestPostgresQuestStore_SaveQuestState(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		s := NewPostgresQuestStore(nil)
		err := s.SaveQuestState(context.Background(), nil)
		require.Error(t, err)
		assert.EqualError(t, err, "quest store database unavailable")

		s = NewPostgresQuestStore(new(mockDBProvider))
		err = s.SaveQuestState(context.Background(), nil)
		require.Error(t, err)
		assert.EqualError(t, err, "quest is required")

		err = s.SaveQuestState(context.Background(), &QuestState{SessionID: "  "})
		require.Error(t, err)
		assert.EqualError(t, err, "quest id is required")
	})

	t.Run("marshalingError", func(t *testing.T) {
		s := NewPostgresQuestStore(new(mockDBProvider))
		err := s.SaveQuestState(context.Background(), &QuestState{
			SessionID: "sess",
			Artifacts: map[string]any{
				"bad": func() {},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "marshal quest state:")
	})

	t.Run("execError", func(t *testing.T) {
		mdb := new(mockDBProvider)
		mdb.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(pgconn.CommandTag{}, assert.AnError).Once()

		s := NewPostgresQuestStore(mdb)
		err := s.SaveQuestState(context.Background(), &QuestState{
			QuestID: "q1",
			ID:      "q1",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "save quest state")
		assert.True(t, mdb.AssertExpectations(t))
	})

	t.Run("success", func(t *testing.T) {
		mdb := new(mockDBProvider)
		var savedArgs []any
		mdb.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(pgconn.CommandTag{}, nil).Run(func(args mock.Arguments) {
			savedArgs = args.Get(2).([]any)
		}).Once()

		s := NewPostgresQuestStore(mdb)
		err := s.SaveQuestState(context.Background(), &QuestState{
			SessionID: "sess-save",
			UserID:    "  123e4567-e89b-12d3-a456-426614174000  ",
		})
		require.NoError(t, err)
		assert.Equal(t, "sess-save", savedArgs[0].(string))
		assert.Equal(t, "123e4567-e89b-12d3-a456-426614174000", savedArgs[1].(string))
		assert.NotZero(t, savedArgs[3].(int64))
	})
}

func TestPostgresQuestStore_LoadQuestState(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		s := NewPostgresQuestStore(nil)
		_, err := s.LoadQuestState(context.Background(), "  ")
		require.Error(t, err)
		assert.EqualError(t, err, "quest store database unavailable")
	})

	t.Run("scanError", func(t *testing.T) {
		mdb := new(mockDBProvider)
		row := new(mockRow)
		mdb.On("QueryRow", mock.Anything, mock.Anything, mock.Anything).Return(row).Once()
		row.On("Scan", mock.Anything).Return(assert.AnError).Once()

		s := NewPostgresQuestStore(mdb)
		_, err := s.LoadQuestState(context.Background(), "q1")
		require.Error(t, err)
		assert.True(t, mdb.AssertExpectations(t))
	})

	t.Run("missingIDsFallbackToQuestID", func(t *testing.T) {
		mdb := new(mockDBProvider)
		row := new(mockRow)
		raw, _ := json.Marshal(&QuestState{Query: "q"})
		mdb.On("QueryRow", mock.Anything, mock.Anything, mock.Anything).Return(row).Once()
		row.On("Scan", mock.Anything).Run(func(args mock.Arguments) {
			*args.Get(0).(*[]byte) = raw
		}).Return(nil).Once()

		s := NewPostgresQuestStore(mdb)
		quest, err := s.LoadQuestState(context.Background(), "q-missing")
		require.NoError(t, err)
		assert.Equal(t, "q-missing", quest.ID)
		assert.Equal(t, "q-missing", quest.SessionID)
		assert.Equal(t, "q", quest.Query)
	})
}

func TestPostgresQuestStore_SaveIteration(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		s := NewPostgresQuestStore(nil)
		err := s.SaveIteration(context.Background(), "   ", IterationRecord{})
		require.Error(t, err)
		assert.EqualError(t, err, "quest store database unavailable")
	})

	t.Run("successUsesCurrentTimeWhenMissing", func(t *testing.T) {
		mdb := new(mockDBProvider)
		before := time.Now().UnixMilli()
		var savedArgs []any
		mdb.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(pgconn.CommandTag{}, nil).Run(func(args mock.Arguments) {
			savedArgs = args.Get(2).([]any)
		}).Once()

		s := NewPostgresQuestStore(mdb)
		err := s.SaveIteration(context.Background(), "q1", IterationRecord{Iteration: 1})
		require.NoError(t, err)
		ts := savedArgs[3].(int64)
		assert.GreaterOrEqual(t, ts, before)
	})

	t.Run("execError", func(t *testing.T) {
		mdb := new(mockDBProvider)
		mdb.On("Exec", mock.Anything, mock.Anything, mock.Anything).Return(pgconn.CommandTag{}, assert.AnError).Once()

		s := NewPostgresQuestStore(mdb)
		err := s.SaveIteration(context.Background(), "q1", IterationRecord{Iteration: 1, Timestamp: time.UnixMilli(1_000_000_000_000)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "save quest iteration")
	})
}

func TestPostgresQuestStore_ValidUUIDHandling(t *testing.T) {
	rawUUID := "550e8400-e29b-41d4-a716-446655440000"
	require.NoError(t, uuid.Validate(rawUUID))
	assert.Equal(t, rawUUID, normalizeOptionalQuestUserID(rawUUID).(string))
}
