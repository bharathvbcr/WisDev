package wisdev

import (
	"testing"
	"time"
)

func TestSteeringBusDeliversSignalsBySession(t *testing.T) {
	ch, unregister := RegisterSteeringChannel("session-steer")
	defer unregister()

	if err := SubmitSteeringSignal("session-steer", SteeringSignal{Type: "focus", Payload: "hippocampal replay"}); err != nil {
		t.Fatalf("SubmitSteeringSignal returned error: %v", err)
	}

	select {
	case signal := <-ch:
		if signal.Type != "focus" || signal.Payload != "hippocampal replay" {
			t.Fatalf("unexpected steering signal: %+v", signal)
		}
		if signal.Timestamp == 0 {
			t.Fatal("expected timestamp to be populated")
		}
	default:
		t.Fatal("expected steering signal to be delivered")
	}
}

func TestSteeringBusQueuesSignalsUntilSessionRegisters(t *testing.T) {
	delivered, err := QueueSteeringSignal("session-queued", SteeringSignal{Type: "focus", Payload: "sleep spindles"})
	if err != nil {
		t.Fatalf("QueueSteeringSignal returned error: %v", err)
	}
	if delivered {
		t.Fatal("expected signal to be queued before a listener registers")
	}

	ch, unregister := RegisterSteeringChannel("session-queued")
	defer unregister()

	select {
	case signal := <-ch:
		if signal.Type != "focus" || signal.Payload != "sleep spindles" {
			t.Fatalf("unexpected queued signal: %+v", signal)
		}
		if signal.Timestamp == 0 {
			t.Fatal("expected timestamp to be populated")
		}
	default:
		t.Fatal("expected queued steering signal to replay on registration")
	}
}

func TestSteeringBusRejectsBlankSession(t *testing.T) {
	if err := SubmitSteeringSignal(" ", SteeringSignal{Type: "focus"}); err == nil {
		t.Fatal("expected blank session to be rejected")
	}
}

func TestSteeringBusReplaysJournaledSignals(t *testing.T) {
	journal := NewRuntimeJournal(nil)
	sessionID := "session-journal-replay"
	journal.Append(RuntimeJournalEntry{
		SessionID: sessionID,
		EventType: EventWisDevSteeringSignal,
		CreatedAt: NowMillis(),
		Payload: map[string]any{
			"type":      "redirect",
			"payload":   "new scope",
			"queries":   []any{"query one"},
			"timestamp": float64(12345),
		},
	})

	if replayed := ReplayJournaledSteeringSignals(sessionID, journal, 10); replayed != 1 {
		t.Fatalf("expected one replayed signal, got %d", replayed)
	}
	ch, unregister := RegisterSteeringChannel(sessionID)
	defer unregister()
	select {
	case signal := <-ch:
		if signal.Type != "redirect" || signal.Payload != "new scope" || len(signal.Queries) != 1 || signal.Queries[0] != "query one" {
			t.Fatalf("unexpected replayed signal: %+v", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("expected replayed steering signal")
	}
}
