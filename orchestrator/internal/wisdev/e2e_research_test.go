package wisdev

import (
	"context"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type mockModel struct {
	Model
}

func (m *mockModel) Generate(ctx context.Context, prompt string) (string, error) {
	return "Mocked generation result", nil
}

func (m *mockModel) GenerateHypotheses(ctx context.Context, query string) ([]string, error) {
	return []string{"CRISPR uses Cas9"}, nil
}

func (m *mockModel) ExtractClaims(ctx context.Context, text string) ([]string, error) {
	return []string{"CRISPR-Cas9 mechanism"}, nil
}

func (m *mockModel) VerifyClaim(ctx context.Context, claim, evidence string) (bool, float64, error) {
	return true, 0.9, nil
}

func (m *mockModel) SynthesizeFindings(ctx context.Context, hypotheses []string, evidence map[string]interface{}) (string, error) {
	return "Mocked synthesis result", nil
}

func (m *mockModel) CritiqueFindings(ctx context.Context, findings []string) (string, error) {
	return "Mocked critique result", nil
}

func (m *mockModel) Name() string    { return "mock-model" }
func (m *mockModel) Tier() ModelTier { return TierStandard }

func TestYOLOOrchestrator_E2E(t *testing.T) {
	ctx := context.Background()

	// Setup dependencies
	mockM := &mockModel{}
	searchReg := search.NewProviderRegistry()
	ragEngine := rag.NewEngine(searchReg, nil)
	store := &mockQuestStore{}

	orchestrator := NewYOLOOrchestrator(
		"test-session",
		"How does CRISPR work?",
		mockM,
		searchReg,
		ragEngine,
		store,
		nil, // db
	)

	// Run the loop
	state, err := orchestrator.Run(ctx)
	if err != nil {
		t.Fatalf("YOLO loop failed: %v", err)
	}

	if state.Status != "complete" {
		t.Errorf("expected status complete, got %s", state.Status)
	}

	if state.Synthesis == nil || state.Synthesis.Sections["main"] == "" {
		t.Errorf("expected synthesis results")
	}
}

type mockQuestStore struct {
	QuestStateStore
}

func (m *mockQuestStore) SaveQuestState(ctx context.Context, quest *QuestState) error {
	return nil
}
