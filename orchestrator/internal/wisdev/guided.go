package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// GuidedFlow handles the sequence of adaptive questions.
type GuidedFlow struct {
	Questions []Question
}

func NewGuidedFlow() *GuidedFlow {
	return &GuidedFlow{
		Questions: DefaultQuestionFlow(),
	}
}

func (f *GuidedFlow) GetNextQuestion(session *Session) (*Question, bool) {
	sequence := f.ensureAdaptiveSequence(session)
	if session.CurrentQuestionIndex >= len(sequence) {
		return nil, false
	}
	question, ok := f.questionForSessionIndex(session, session.CurrentQuestionIndex)
	if !ok {
		return nil, false
	}
	return question, true
}

func (f *GuidedFlow) ProcessAnswer(ctx context.Context, session *Session, answer Answer) error {
	_ = ctx
	sequence := f.ensureAdaptiveSequence(session)

	// Basic validation: ensure question ID matches current expected question
	if session.CurrentQuestionIndex >= len(sequence) {
		return fmt.Errorf("no more questions in current flow")
	}

	expectedID := sequence[session.CurrentQuestionIndex]
	if answer.QuestionID != expectedID {
		return fmt.Errorf("unexpected question ID: expected %s, got %s", expectedID, answer.QuestionID)
	}

	// Store answer
	if session.Answers == nil {
		session.Answers = make(map[string]Answer)
	}
	question, _ := questionByID()[strings.TrimSpace(answer.QuestionID)]
	normalizedAnswer := NormalizeAnswerForQuestion(answer, question.IsMultiSelect)
	session.Answers[normalizedAnswer.QuestionID] = normalizedAnswer

	// Advanced logic: if q1_domain was answered, we might adapt the flow
	if normalizedAnswer.QuestionID == "q1_domain" && len(normalizedAnswer.Values) > 0 {
		session.DetectedDomain = normalizedAnswer.Values[0]
		if len(normalizedAnswer.Values) > 1 {
			session.SecondaryDomains = normalizedAnswer.Values[1:]
		}
		f.ensureAdaptiveSequence(session)
	}

	// Increment index
	session.CurrentQuestionIndex++

	// Check if we should transition to SessionComplete or SessionGeneratingTree
	if session.CurrentQuestionIndex >= len(session.QuestionSequence) {
		session.Status = SessionGeneratingTree
	} else {
		session.Status = SessionQuestioning
	}

	return nil
}

func (f *GuidedFlow) ensureAdaptiveSequence(session *Session) []string {
	if session == nil {
		return nil
	}

	query := ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery)
	if query == "" {
		// Emit a structured warning so Cloud Logging can surface the session
		// that had no resolvable query at question-sequence build time.
		// The function continues using the default complexity score — the
		// user still receives the standard question sequence.
		slog.Warn("guided_flow: ensureAdaptiveSequence called with empty session query — using default complexity score",
			"component", "wisdev.guided",
			"operation", "ensureAdaptiveSequence",
			"stage", "empty_session_query",
			"session_id", session.ID,
		)
	}
	complexity := EstimateComplexityScore(query)
	sequence, _, _ := BuildAdaptiveQuestionSequence(complexity, session.DetectedDomain)
	session.QuestionSequence = sequence
	return sequence
}

func (f *GuidedFlow) questionForSessionIndex(session *Session, index int) (*Question, bool) {
	sequence := f.ensureAdaptiveSequence(session)
	if index < 0 || index >= len(sequence) {
		return nil, false
	}

	questionsByID := questionByID()
	question, ok := questionsByID[sequence[index]]
	if !ok {
		return nil, false
	}

	return &question, true
}
