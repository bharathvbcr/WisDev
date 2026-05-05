package wisdev

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type nilGetSessionStore struct {
	putTarget SessionStore
}

func (s *nilGetSessionStore) Get(context.Context, string) (*AgentSession, error) {
	return nil, nil
}

func (s *nilGetSessionStore) Put(ctx context.Context, session *AgentSession, ttl time.Duration) error {
	if s.putTarget != nil {
		return s.putTarget.Put(ctx, session, ttl)
	}
	return nil
}

func (s *nilGetSessionStore) Delete(context.Context, string) error {
	return nil
}

func (s *nilGetSessionStore) List(context.Context, string) ([]*AgentSession, error) {
	return nil, nil
}

type errorGetSessionStore struct{}

func (s *errorGetSessionStore) Get(context.Context, string) (*AgentSession, error) {
	return nil, errors.New("primary_failed")
}

func (s *errorGetSessionStore) Put(context.Context, *AgentSession, time.Duration) error {
	return errors.New("primary_failed")
}

func (s *errorGetSessionStore) Delete(context.Context, string) error {
	return nil
}

func (s *errorGetSessionStore) List(context.Context, string) ([]*AgentSession, error) {
	return nil, errors.New("primary_failed")
}

func TestInMemorySessionStore(t *testing.T) {
	is := assert.New(t)
	store := NewInMemorySessionStore()
	ctx := context.Background()

	t.Run("put and get", func(t *testing.T) {
		sess := &AgentSession{SessionID: "s1", UserID: "u1"}
		err := store.Put(ctx, sess, 0)
		is.NoError(err)

		got, err := store.Get(ctx, "s1")
		is.NoError(err)
		is.Equal("u1", got.UserID)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := store.Get(ctx, "nonexistent")
		is.Error(err)
		is.Equal(errSessionNotFound, err)
	})

	t.Run("expiration", func(t *testing.T) {
		sess := &AgentSession{SessionID: "s-exp", UserID: "u1"}
		err := store.Put(ctx, sess, 1*time.Millisecond)
		is.NoError(err)

		time.Sleep(2 * time.Millisecond)
		_, err = store.Get(ctx, "s-exp")
		is.Error(err)
	})

	t.Run("delete", func(t *testing.T) {
		sess := &AgentSession{SessionID: "s-del", UserID: "u1"}
		_ = store.Put(ctx, sess, 0)
		err := store.Delete(ctx, "s-del")
		is.NoError(err)
		_, err = store.Get(ctx, "s-del")
		is.Error(err)
	})

	t.Run("list", func(t *testing.T) {
		store := NewInMemorySessionStore()
		_ = store.Put(ctx, &AgentSession{SessionID: "s1", UserID: "u1"}, 0)
		_ = store.Put(ctx, &AgentSession{SessionID: "s2", UserID: "u1"}, 0)
		_ = store.Put(ctx, &AgentSession{SessionID: "s3", UserID: "u2"}, 0)

		list, err := store.List(ctx, "u1")
		is.NoError(err)
		is.Len(list, 2)
	})

	t.Run("get returns deep clone", func(t *testing.T) {
		store := NewInMemorySessionStore()
		err := store.Put(ctx, &AgentSession{
			SessionID: "s-clone",
			UserID:    "u1",
			Plan: &PlanState{
				PlanID:            "p1",
				PendingApprovalID: "approval-original",
				CompletedStepIDs:  map[string]bool{"s1": true},
			},
		}, 0)
		is.NoError(err)

		got, err := store.Get(ctx, "s-clone")
		is.NoError(err)
		got.Plan.PendingApprovalID = "approval-mutated"
		got.Plan.CompletedStepIDs["s2"] = true

		again, err := store.Get(ctx, "s-clone")
		is.NoError(err)
		is.Equal("approval-original", again.Plan.PendingApprovalID)
		is.False(again.Plan.CompletedStepIDs["s2"])
	})

	t.Run("list returns deep clones", func(t *testing.T) {
		store := NewInMemorySessionStore()
		err := store.Put(ctx, &AgentSession{
			SessionID: "s-list-clone",
			UserID:    "u1",
			Plan: &PlanState{
				PlanID:           "p-list",
				CompletedStepIDs: map[string]bool{"s1": true},
			},
		}, 0)
		is.NoError(err)

		list, err := store.List(ctx, "u1")
		is.NoError(err)
		is.Len(list, 1)
		list[0].Plan.CompletedStepIDs["s2"] = true

		again, err := store.Get(ctx, "s-list-clone")
		is.NoError(err)
		is.False(again.Plan.CompletedStepIDs["s2"])
	})
}

func TestFallbackSessionStore(t *testing.T) {
	is := assert.New(t)
	p := NewInMemorySessionStore()
	f := NewInMemorySessionStore()
	store := NewFallbackSessionStore(p, f)
	ctx := context.Background()

	t.Run("gets from primary", func(t *testing.T) {
		_ = p.Put(ctx, &AgentSession{SessionID: "s1", UserID: "p"}, 0)
		got, err := store.Get(ctx, "s1")
		is.NoError(err)
		is.Equal("p", got.UserID)
	})

	t.Run("gets from fallback", func(t *testing.T) {
		_ = f.Put(ctx, &AgentSession{SessionID: "s2", UserID: "f"}, 0)
		got, err := store.Get(ctx, "s2")
		is.NoError(err)
		is.Equal("f", got.UserID)
	})

	t.Run("puts to both", func(t *testing.T) {
		sess := &AgentSession{SessionID: "s3", UserID: "both"}
		err := store.Put(ctx, sess, 0)
		is.NoError(err)

		gotP, _ := p.Get(ctx, "s3")
		is.Equal("both", gotP.UserID)
		gotF, _ := f.Get(ctx, "s3")
		is.Equal("both", gotF.UserID)
	})

	t.Run("nil primary result falls back", func(t *testing.T) {
		fallback := NewInMemorySessionStore()
		_ = fallback.Put(ctx, &AgentSession{SessionID: "s-nil-primary", UserID: "fallback"}, 0)
		store := NewFallbackSessionStore(&nilGetSessionStore{}, fallback)

		got, err := store.Get(ctx, "s-nil-primary")
		is.NoError(err)
		is.Equal("fallback", got.UserID)
	})

	t.Run("nil primary and missing fallback is not found", func(t *testing.T) {
		store := NewFallbackSessionStore(&nilGetSessionStore{}, NewInMemorySessionStore())

		got, err := store.Get(ctx, "s-missing")
		is.Nil(got)
		is.ErrorIs(err, errSessionNotFound)
	})

	t.Run("nil fallback is not found", func(t *testing.T) {
		store := NewFallbackSessionStore(&errorGetSessionStore{}, nil)

		got, err := store.Get(ctx, "s-missing")
		is.Nil(got)
		is.ErrorIs(err, errSessionNotFound)
	})

	t.Run("nil primary put falls back without panic", func(t *testing.T) {
		fallback := NewInMemorySessionStore()
		store := NewFallbackSessionStore(nil, fallback)

		err := store.Put(ctx, &AgentSession{SessionID: "s-nil-put", UserID: "u1"}, 0)
		is.NoError(err)
		got, err := fallback.Get(ctx, "s-nil-put")
		is.NoError(err)
		is.Equal("u1", got.UserID)
	})

	t.Run("nil stores return unavailable on put and list", func(t *testing.T) {
		store := NewFallbackSessionStore(nil, nil)

		is.ErrorIs(store.Put(ctx, &AgentSession{SessionID: "s-none"}, 0), errSessionStoreUnavailable)
		_, err := store.List(ctx, "u1")
		is.ErrorIs(err, errSessionStoreUnavailable)
		is.NoError(store.Delete(ctx, "s-none"))
	})
}

func TestPostgresSessionStore_ErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("nil db returns error", func(t *testing.T) {
		s := NewPostgresSessionStore(nil)
		_, err := s.Get(ctx, "id")
		assert.Error(t, err)
		assert.Equal(t, "db_not_available", err.Error())

		assert.Error(t, s.Put(ctx, &AgentSession{}, 0))
		_, err = s.List(ctx, "u1")
		assert.Error(t, err)
	})
}

func TestRedisSessionStore_NilClient(t *testing.T) {
	ctx := context.Background()
	s := NewRedisSessionStore(nil)

	t.Run("nil client handling", func(t *testing.T) {
		_, err := s.Get(ctx, "id")
		assert.ErrorIs(t, err, errSessionNotFound)

		assert.Error(t, s.Put(ctx, &AgentSession{}, 0))

		err = s.Delete(ctx, "id")
		assert.NoError(t, err)

		list, err := s.List(ctx, "u1")
		assert.NoError(t, err)
		assert.Empty(t, list)
	})
}
