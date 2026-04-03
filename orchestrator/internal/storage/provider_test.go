package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInMemoryProvider(t *testing.T) {
	ctx := context.Background()
	p := NewInMemoryProvider()

	t.Run("save and get", func(t *testing.T) {
		s := &Session{SessionID: "s1", UserID: "u1", Status: "active"}
		if err := p.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}

		got, err := p.GetSession(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if got.SessionID != "s1" {
			t.Errorf("expected s1, got %s", got.SessionID)
		}
	})

	t.Run("get not found", func(t *testing.T) {
		_, err := p.GetSession(ctx, "missing")
		if err == nil {
			t.Error("expected error for missing session")
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := &Session{SessionID: "s2", UserID: "u1"}
		if err := p.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := p.DeleteSession(ctx, "s2"); err != nil {
			t.Fatal(err)
		}
		_, err := p.GetSession(ctx, "s2")
		if err == nil {
			t.Error("expected error after delete")
		}
	})

	t.Run("list by user", func(t *testing.T) {
		p := NewInMemoryProvider()
		p.SaveSession(ctx, &Session{SessionID: "list-s3", UserID: "u1"})
		p.SaveSession(ctx, &Session{SessionID: "list-s4", UserID: "u2"})

		sessions, err := p.ListSessions(ctx, "u1")
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 1 {
			t.Errorf("expected 1 session for u1, got %d", len(sessions))
		}
	})

	t.Run("checkpoint", func(t *testing.T) {
		p := NewInMemoryProvider()
		s := &Session{SessionID: "cp-s5"}
		p.SaveSession(ctx, s)
		data := []byte("checkpoint-data")
		if err := p.SaveCheckpoint(ctx, "cp-s5", data); err != nil {
			t.Fatal(err)
		}
		got, err := p.LoadCheckpoint(ctx, "cp-s5")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "checkpoint-data" {
			t.Errorf("expected checkpoint-data, got %s", string(got))
		}
	})

	t.Run("close", func(t *testing.T) {
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSQLiteProvider(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	p, err := NewSQLiteProvider(dbPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	t.Run("save and get", func(t *testing.T) {
		s := &Session{SessionID: "s1", UserID: "u1", Status: "active"}
		if err := p.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}

		got, err := p.GetSession(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if got.SessionID != "s1" {
			t.Errorf("expected s1, got %s", got.SessionID)
		}
	})

	t.Run("persistence across instances", func(t *testing.T) {
		time.Sleep(100 * time.Millisecond)
		p2, err := NewSQLiteProvider(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer p2.Close()

		got, err := p2.GetSession(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if got.SessionID != "s1" {
			t.Errorf("expected s1 after reload, got %s", got.SessionID)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := p.DeleteSession(ctx, "s1"); err != nil {
			t.Fatal(err)
		}
		_, err := p.GetSession(ctx, "s1")
		if err == nil {
			t.Error("expected error after delete")
		}
	})

	t.Run("checkpoint", func(t *testing.T) {
		p.SaveSession(ctx, &Session{SessionID: "s2"})
		data := []byte("test-checkpoint")
		if err := p.SaveCheckpoint(ctx, "s2", data); err != nil {
			t.Fatal(err)
		}
		got, err := p.LoadCheckpoint(ctx, "s2")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "test-checkpoint" {
			t.Errorf("expected test-checkpoint, got %s", string(got))
		}
	})
}

func TestNewProvider(t *testing.T) {
	t.Run("memory default", func(t *testing.T) {
		p, err := NewProvider("", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(*InMemoryProvider); !ok {
			t.Error("expected InMemoryProvider for empty type")
		}
	})

	t.Run("memory explicit", func(t *testing.T) {
		p, err := NewProvider("memory", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(*InMemoryProvider); !ok {
			t.Error("expected InMemoryProvider for memory type")
		}
	})

	t.Run("sqlite", func(t *testing.T) {
		dir := t.TempDir()
		p, err := NewProvider("sqlite", filepath.Join(dir, "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		if _, ok := p.(*SQLiteProvider); !ok {
			t.Error("expected SQLiteProvider for sqlite type")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		_, err := NewProvider("postgres", "")
		if err == nil {
			t.Error("expected error for unknown type")
		}
	})
}

func TestSessionTimestamps(t *testing.T) {
	ctx := context.Background()
	p := NewInMemoryProvider()

	s := &Session{SessionID: "s1"}
	before := time.Now()
	if err := p.SaveSession(ctx, s); err != nil {
		t.Fatal(err)
	}

	if !s.CreatedAt.After(before.Add(-time.Second)) {
		t.Error("expected CreatedAt to be set to recent time")
	}
	if !s.UpdatedAt.After(before.Add(-time.Second)) {
		t.Error("expected UpdatedAt to be set to recent time")
	}
}

func TestFileCleanup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cleanup.db")

	p, err := NewSQLiteProvider(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected db file to exist: %v", err)
	}
}
