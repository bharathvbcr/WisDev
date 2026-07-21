package wisdev

import (
	"context"
	"iter"
	"testing"

	"google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
)

func TestExecuteResearchWorkersUsesOfficialADKParallelRunnerMetadata(t *testing.T) {
	markerAgent, err := agent.New(agent.Config{
		Name:        "wisdev-adk-marker",
		Description: "test marker agent for enabling the official ADK research swarm path",
		Run: func(agent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(func(*adksession.Event, error) bool) {}
		},
	})
	if err != nil {
		t.Fatalf("failed to create ADK marker agent: %v", err)
	}

	rt := NewUnifiedResearchRuntime(nil, nil, nil, nil, &ADKRuntime{
		Config: DefaultADKRuntimeConfig(),
		Agent:  markerAgent,
	})
	state := newResearchSessionState("sleep and memory", "neuroscience", "session-adk-swarm", ResearchExecutionPlaneMultiAgent)
	session := &AgentSession{SessionID: state.SessionID, Query: state.Query, DetectedDomain: state.Domain}
	var events []PlanExecutionEvent

	queries := rt.executeResearchWorkers(context.Background(), state, session, state.Query, state.Domain, string(WisDevModeGuided), false, 0, false, func(event PlanExecutionEvent) {
		events = append(events, event)
	})

	if len(queries) == 0 {
		t.Fatalf("expected ADK research swarm to return worker queries")
	}
	var adkEvent *PlanExecutionEvent
	for idx := range events {
		if events[idx].Payload["stage"] == "completed_adk_parallel" {
			adkEvent = &events[idx]
			break
		}
	}
	if adkEvent == nil {
		t.Fatalf("expected completed_adk_parallel event, got %#v", events)
	}
	if adkEvent.Payload["adkRuntime"] != "google.golang.org/adk/v2" {
		t.Fatalf("expected official ADK runtime marker, got %#v", adkEvent.Payload)
	}
	if adkEvent.Payload["adkWorkflowAgent"] != "parallelagent" {
		t.Fatalf("expected ADK parallel agent marker, got %#v", adkEvent.Payload)
	}
	if adkEvent.Payload["adkWorkflowStage"] != "parallel_fanout_gather" {
		t.Fatalf("expected ADK fan-out/gather marker, got %#v", adkEvent.Payload)
	}
	if adkEvent.Payload["adkRunnerExecuted"] != true {
		t.Fatalf("expected ADK runner execution marker, got %#v", adkEvent.Payload)
	}
	if adkEvent.Payload["adkInvocationId"] == "" || adkEvent.Payload["adkEventAuthor"] == "" {
		t.Fatalf("expected ADK invocation metadata, got %#v", adkEvent.Payload)
	}

	scoutEventIdx := -1
	verifierEventIdx := -1
	synthesizerEventIdx := -1
	for idx := range events {
		switch events[idx].Payload["role"] {
		case string(ResearchWorkerScout):
			scoutEventIdx = idx
		case string(ResearchWorkerIndependentVerifier):
			verifierEventIdx = idx
		case string(ResearchWorkerSynthesizer):
			synthesizerEventIdx = idx
		}
	}
	if scoutEventIdx < 0 || verifierEventIdx < 0 || synthesizerEventIdx < 0 {
		t.Fatalf("expected scout, verifier, and synthesizer ADK events, got %#v", events)
	}
	if !(scoutEventIdx < verifierEventIdx && verifierEventIdx < synthesizerEventIdx) {
		t.Fatalf("expected ADK waves to run gather -> verifier -> synthesizer, got scout=%d verifier=%d synthesizer=%d", scoutEventIdx, verifierEventIdx, synthesizerEventIdx)
	}

	verifierIdx := findResearchWorkerIndex(state.Workers, ResearchWorkerIndependentVerifier)
	if verifierIdx < 0 {
		t.Fatalf("expected independent verifier worker in state")
	}
	openLedger, ok := state.Workers[verifierIdx].Artifacts["blackboardOpenLedger"].(int)
	if !ok || openLedger == 0 {
		t.Fatalf("expected verifier to consume gather-wave blackboard, got %#v", state.Workers[verifierIdx].Artifacts)
	}

	synthesizerIdx := findResearchWorkerIndex(state.Workers, ResearchWorkerSynthesizer)
	if synthesizerIdx < 0 {
		t.Fatalf("expected synthesizer worker in state")
	}
	if _, ok := state.Workers[synthesizerIdx].Artifacts["blackboardSynthesisGate"].(string); !ok {
		t.Fatalf("expected synthesizer to consume verifier-wave blackboard, got %#v", state.Workers[synthesizerIdx].Artifacts)
	}
}
