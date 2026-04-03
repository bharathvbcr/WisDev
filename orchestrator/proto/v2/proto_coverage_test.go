package wisdevv2_test

import (
	"context"
	"testing"

	pb "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/v2"
)

// ==========================================
// SessionStatus enum
// ==========================================

func TestGaps_SessionStatus_Methods(t *testing.T) {
	statuses := []pb.SessionStatus{
		pb.SessionStatus_SESSION_STATUS_UNSPECIFIED,
		pb.SessionStatus_QUESTIONING,
		pb.SessionStatus_GENERATING_TREE,
		pb.SessionStatus_EDITING_TREE,
		pb.SessionStatus_EXECUTING_PLAN,
		pb.SessionStatus_PAUSED,
		pb.SessionStatus_COMPLETE,
		pb.SessionStatus_FAILED,
	}
	for _, s := range statuses {
		str := s.String()
		if str == "" {
			t.Errorf("SessionStatus %v has empty String()", s)
		}
		_ = s.Number()
		_ = s.Descriptor()
		_ = s.Type()
		p := s.Enum()
		if p == nil {
			t.Errorf("SessionStatus %v Enum() returned nil", s)
		}
	}
}

// ==========================================
// QuestionStopReason enum
// ==========================================

func TestGaps_QuestionStopReason_Methods(t *testing.T) {
	reasons := []pb.QuestionStopReason{
		pb.QuestionStopReason_QUESTION_STOP_REASON_UNSPECIFIED,
		pb.QuestionStopReason_EVIDENCE_SUFFICIENT,
		pb.QuestionStopReason_CLARIFICATION_BUDGET_REACHED,
		pb.QuestionStopReason_USER_PROCEED,
	}
	for _, r := range reasons {
		str := r.String()
		if str == "" {
			t.Errorf("QuestionStopReason %v has empty String()", r)
		}
		_ = r.Number()
		_ = r.Descriptor()
		_ = r.Type()
		p := r.Enum()
		if p == nil {
			t.Errorf("QuestionStopReason %v Enum() returned nil", r)
		}
	}
}

// ==========================================
// RiskLevel enum
// ==========================================

func TestGaps_RiskLevel_Methods(t *testing.T) {
	levels := []pb.RiskLevel{
		pb.RiskLevel_RISK_LEVEL_UNSPECIFIED,
		pb.RiskLevel_LOW,
		pb.RiskLevel_MEDIUM,
		pb.RiskLevel_HIGH,
	}
	for _, l := range levels {
		str := l.String()
		if str == "" {
			t.Errorf("RiskLevel %v has empty String()", l)
		}
		_ = l.Number()
		_ = l.Descriptor()
		_ = l.Type()
		p := l.Enum()
		if p == nil {
			t.Errorf("RiskLevel %v Enum() returned nil", l)
		}
	}
}

// ==========================================
// ExecutionTarget enum
// ==========================================

func TestGaps_ExecutionTarget_Methods(t *testing.T) {
	targets := []pb.ExecutionTarget{
		pb.ExecutionTarget_EXECUTION_TARGET_UNSPECIFIED,
		pb.ExecutionTarget_GO_NATIVE,
		pb.ExecutionTarget_PYTHON_CAPABILITY,
		pb.ExecutionTarget_PYTHON_SANDBOX,
	}
	for _, target := range targets {
		str := target.String()
		if str == "" {
			t.Errorf("ExecutionTarget %v has empty String()", target)
		}
		_ = target.Number()
		_ = target.Descriptor()
		_ = target.Type()
		p := target.Enum()
		if p == nil {
			t.Errorf("ExecutionTarget %v Enum() returned nil", target)
		}
	}
}

// ==========================================
// QualityMode enum
// ==========================================

func TestGaps_QualityMode_Methods(t *testing.T) {
	modes := []pb.QualityMode{
		pb.QualityMode_QUALITY_UNSPECIFIED,
		pb.QualityMode_QUALITY_QUICK,
		pb.QualityMode_QUALITY_BALANCED,
		pb.QualityMode_QUALITY_THOROUGH,
	}
	for _, m := range modes {
		_ = m.String()
		_ = m.Number()
		_ = m.Descriptor()
		_ = m.Type()
		_ = m.Enum()
	}
}

// ==========================================
// UnimplementedAgentGatewayServer
// ==========================================

func TestGaps_UnimplementedAgentGatewayServer(t *testing.T) {
	var srv pb.UnimplementedAgentGatewayServer
	ctx := context.Background()

	_, err := srv.CreateSession(ctx, &pb.CreateSessionRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from CreateSession")
	}

	_, err = srv.GetSession(ctx, &pb.GetSessionRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from GetSession")
	}

	_, err = srv.ResumeSession(ctx, &pb.ResumeSessionRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from ResumeSession")
	}

	_, err = srv.GetNextQuestion(ctx, &pb.GetNextQuestionRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from GetNextQuestion")
	}

	_, err = srv.SubmitAnswer(ctx, &pb.SubmitAnswerRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from SubmitAnswer")
	}

	_, err = srv.CancelPlan(ctx, &pb.CancelPlanRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from CancelPlan")
	}

	_, err = srv.ListTools(ctx, &pb.ListToolsRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from ListTools")
	}

	_, err = srv.InvokeTool(ctx, &pb.InvokeToolRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from InvokeTool")
	}

	_, err = srv.SaveCheckpoint(ctx, &pb.SaveCheckpointRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from SaveCheckpoint")
	}

	_, err = srv.LoadCheckpoint(ctx, &pb.LoadCheckpointRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from LoadCheckpoint")
	}

	_, err = srv.ToolSearch(ctx, &pb.ToolSearchRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from ToolSearch")
	}

	_, err = srv.ProgrammaticLoop(ctx, &pb.ProgrammaticLoopRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from ProgrammaticLoop")
	}

	_, err = srv.StructuredOutput(ctx, &pb.StructuredOutputRequest{})
	if err == nil {
		t.Error("expected Unimplemented error from StructuredOutput")
	}
	// ExecutePlan has streaming signature — skip
}

// ==========================================
// Message type instantiation
// ==========================================

func TestGaps_MessageTypes_Instantiation(t *testing.T) {
	// Verify key message types can be instantiated without panic
	_ = &pb.CreateSessionRequest{}
	_ = &pb.CreateSessionResponse{}
	_ = &pb.GetSessionRequest{}
	_ = &pb.GetSessionResponse{}
	_ = &pb.ResumeSessionRequest{}
	_ = &pb.GetNextQuestionRequest{}
	_ = &pb.GetNextQuestionResponse{}
	_ = &pb.SubmitAnswerRequest{}
	_ = &pb.SubmitAnswerResponse{}
	_ = &pb.CancelPlanRequest{}
	_ = &pb.CancelPlanResponse{}
	_ = &pb.ListToolsRequest{}
	_ = &pb.ListToolsResponse{}
	_ = &pb.InvokeToolRequest{}
	_ = &pb.InvokeToolResponse{}
	_ = &pb.SaveCheckpointRequest{}
	_ = &pb.SaveCheckpointResponse{}
	_ = &pb.LoadCheckpointRequest{}
	_ = &pb.LoadCheckpointResponse{}
	_ = &pb.ToolSearchRequest{}
	_ = &pb.ToolSearchResponse{}
	_ = &pb.ProgrammaticLoopRequest{}
	_ = &pb.ProgrammaticLoopResponse{}
	_ = &pb.StructuredOutputRequest{}
	_ = &pb.StructuredOutputResponse{}
}

// ==========================================
// Full method name constants
// ==========================================

func TestGaps_FullMethodNames(t *testing.T) {
	names := []string{
		pb.AgentGateway_CreateSession_FullMethodName,
		pb.AgentGateway_GetSession_FullMethodName,
		pb.AgentGateway_ResumeSession_FullMethodName,
		pb.AgentGateway_GetNextQuestion_FullMethodName,
		pb.AgentGateway_SubmitAnswer_FullMethodName,
		pb.AgentGateway_ExecutePlan_FullMethodName,
		pb.AgentGateway_CancelPlan_FullMethodName,
		pb.AgentGateway_ListTools_FullMethodName,
		pb.AgentGateway_InvokeTool_FullMethodName,
		pb.AgentGateway_SaveCheckpoint_FullMethodName,
		pb.AgentGateway_LoadCheckpoint_FullMethodName,
		pb.AgentGateway_ToolSearch_FullMethodName,
		pb.AgentGateway_ProgrammaticLoop_FullMethodName,
		pb.AgentGateway_StructuredOutput_FullMethodName,
	}
	for _, name := range names {
		if name == "" {
			t.Errorf("expected non-empty full method name")
		}
	}
}

// ==========================================
// PlanStepStatus enum (not covered previously)
// ==========================================

func TestGaps_PlanStepStatus_Methods(t *testing.T) {
	statuses := []pb.PlanStepStatus{
		pb.PlanStepStatus_STEP_PENDING,
		pb.PlanStepStatus_STEP_RUNNING,
		pb.PlanStepStatus_STEP_DONE,
		pb.PlanStepStatus_STEP_SKIPPED,
		pb.PlanStepStatus_STEP_FAILED,
	}
	for _, s := range statuses {
		str := s.String()
		if str == "" {
			t.Errorf("PlanStepStatus %v has empty String()", s)
		}
		_ = s.Number()
		_ = s.Descriptor()
		_ = s.Type()
		p := s.Enum()
		if p == nil {
			t.Errorf("PlanStepStatus %v Enum() returned nil", s)
		}
		// Cover the deprecated EnumDescriptor method.
		b, idx := s.EnumDescriptor()
		if b == nil || len(idx) == 0 {
			t.Errorf("PlanStepStatus EnumDescriptor returned unexpected values")
		}
	}
}

// ==========================================
// ClaimVerdict enum (not covered previously)
// ==========================================

func TestGaps_ClaimVerdict_Methods(t *testing.T) {
	verdicts := []pb.ClaimVerdict{
		pb.ClaimVerdict_VERDICT_UNSPECIFIED,
		pb.ClaimVerdict_VERDICT_SUPPORTED,
		pb.ClaimVerdict_VERDICT_CONTRADICTED,
		pb.ClaimVerdict_VERDICT_INSUFFICIENT_EVIDENCE,
	}
	for _, v := range verdicts {
		str := v.String()
		if str == "" {
			t.Errorf("ClaimVerdict %v has empty String()", v)
		}
		_ = v.Number()
		_ = v.Descriptor()
		_ = v.Type()
		p := v.Enum()
		if p == nil {
			t.Errorf("ClaimVerdict %v Enum() returned nil", v)
		}
		b, idx := v.EnumDescriptor()
		if b == nil || len(idx) == 0 {
			t.Errorf("ClaimVerdict EnumDescriptor returned unexpected values")
		}
	}
}

// ==========================================
// ModelTier enum (not covered previously)
// ==========================================

func TestGaps_ModelTier_Methods(t *testing.T) {
	tiers := []pb.ModelTier{
		pb.ModelTier_TIER_LOCAL,
		pb.ModelTier_TIER_FAST,
		pb.ModelTier_TIER_DEEP,
		pb.ModelTier_TIER_CLAUDE,
	}
	for _, tier := range tiers {
		str := tier.String()
		if str == "" {
			t.Errorf("ModelTier %v has empty String()", tier)
		}
		_ = tier.Number()
		_ = tier.Descriptor()
		_ = tier.Type()
		p := tier.Enum()
		if p == nil {
			t.Errorf("ModelTier %v Enum() returned nil", tier)
		}
		b, idx := tier.EnumDescriptor()
		if b == nil || len(idx) == 0 {
			t.Errorf("ModelTier EnumDescriptor returned unexpected values")
		}
	}
}

// ==========================================
// Deprecated EnumDescriptor for existing enums
// ==========================================

func TestGaps_EnumDescriptors_Deprecated(t *testing.T) {
	// Cover the deprecated EnumDescriptor() method for all enums tested previously.
	b, idx := pb.SessionStatus_SESSION_STATUS_UNSPECIFIED.EnumDescriptor()
	if b == nil || len(idx) == 0 {
		t.Error("SessionStatus EnumDescriptor returned unexpected values")
	}

	b, idx = pb.QuestionStopReason_QUESTION_STOP_REASON_UNSPECIFIED.EnumDescriptor()
	if b == nil || len(idx) == 0 {
		t.Error("QuestionStopReason EnumDescriptor returned unexpected values")
	}

	b, idx = pb.RiskLevel_RISK_LEVEL_UNSPECIFIED.EnumDescriptor()
	if b == nil || len(idx) == 0 {
		t.Error("RiskLevel EnumDescriptor returned unexpected values")
	}

	b, idx = pb.ExecutionTarget_EXECUTION_TARGET_UNSPECIFIED.EnumDescriptor()
	if b == nil || len(idx) == 0 {
		t.Error("ExecutionTarget EnumDescriptor returned unexpected values")
	}

	b, idx = pb.QualityMode_QUALITY_UNSPECIFIED.EnumDescriptor()
	if b == nil || len(idx) == 0 {
		t.Error("QualityMode EnumDescriptor returned unexpected values")
	}
}

// ==========================================
// Message Reset/String/ProtoMessage/ProtoReflect methods
// ==========================================

func TestGaps_MessageMethods_Core(t *testing.T) {
	// AgentSession
	s := &pb.AgentSession{}
	s.Reset()
	_ = s.String()
	s.ProtoMessage()
	_ = s.ProtoReflect()

	// QuestionStateSummary
	q := &pb.QuestionStateSummary{}
	q.Reset()
	_ = q.String()
	q.ProtoMessage()
	_ = q.ProtoReflect()

	// ToolDefinition
	td := &pb.ToolDefinition{}
	td.Reset()
	_ = td.String()
	td.ProtoMessage()
	_ = td.ProtoReflect()

	// PlanExecutionUpdate
	peu := &pb.PlanExecutionUpdate{}
	peu.Reset()
	_ = peu.String()
	peu.ProtoMessage()
	_ = peu.ProtoReflect()

	// StepStarted
	ss := &pb.StepStarted{}
	ss.Reset()
	_ = ss.String()
	ss.ProtoMessage()
	_ = ss.ProtoReflect()

	// StepCompleted
	sc := &pb.StepCompleted{}
	sc.Reset()
	_ = sc.String()
	sc.ProtoMessage()
	_ = sc.ProtoReflect()

	// StepFailed
	sf := &pb.StepFailed{}
	sf.Reset()
	_ = sf.String()
	sf.ProtoMessage()
	_ = sf.ProtoReflect()

	// PlanRevised
	pr := &pb.PlanRevised{}
	pr.Reset()
	_ = pr.String()
	pr.ProtoMessage()
	_ = pr.ProtoReflect()

	// PaperFound
	pf := &pb.PaperFound{}
	pf.Reset()
	_ = pf.String()
	pf.ProtoMessage()
	_ = pf.ProtoReflect()

	// Progress
	prog := &pb.Progress{}
	prog.Reset()
	_ = prog.String()
	prog.ProtoMessage()
	_ = prog.ProtoReflect()

	// ConfirmationRequired
	cr := &pb.ConfirmationRequired{}
	cr.Reset()
	_ = cr.String()
	cr.ProtoMessage()
	_ = cr.ProtoReflect()

	// ConfidenceReport
	conf := &pb.ConfidenceReport{}
	conf.Reset()
	_ = conf.String()
	conf.ProtoMessage()
	_ = conf.ProtoReflect()

	// ModelRequirements
	mr := &pb.ModelRequirements{}
	mr.Reset()
	_ = mr.String()
	mr.ProtoMessage()
	_ = mr.ProtoReflect()
}

func TestGaps_MessageMethods_RequestResponse(t *testing.T) {
	messages := []interface {
		ProtoMessage()
		Reset()
		String() string
	}{
		&pb.CreateSessionRequest{},
		&pb.CreateSessionResponse{},
		&pb.GetSessionRequest{},
		&pb.GetSessionResponse{},
		&pb.ResumeSessionRequest{},
		&pb.ResumeSessionResponse{},
		&pb.GetNextQuestionRequest{},
		&pb.GetNextQuestionResponse{},
		&pb.SubmitAnswerRequest{},
		&pb.SubmitAnswerResponse{},
		&pb.CancelPlanRequest{},
		&pb.CancelPlanResponse{},
		&pb.ListToolsRequest{},
		&pb.ListToolsResponse{},
		&pb.InvokeToolRequest{},
		&pb.InvokeToolResponse{},
		&pb.SaveCheckpointRequest{},
		&pb.SaveCheckpointResponse{},
		&pb.LoadCheckpointRequest{},
		&pb.LoadCheckpointResponse{},
		&pb.ToolSearchRequest{},
		&pb.ToolSearchResponse{},
		&pb.ProgrammaticLoopRequest{},
		&pb.ProgrammaticLoopResponse{},
		&pb.StructuredOutputRequest{},
		&pb.StructuredOutputResponse{},
	}
	for _, m := range messages {
		m.Reset()
		_ = m.String()
		m.ProtoMessage()
	}
}

// ==========================================
// Getter methods for AgentSession
// ==========================================

func TestGaps_AgentSession_Getters(t *testing.T) {
	s := &pb.AgentSession{
		SessionId:           "sid-1",
		UserId:              "uid-1",
		Status:              pb.SessionStatus_QUESTIONING,
		QuestionStopReason:  pb.QuestionStopReason_USER_PROCEED,
		SchemaVersion:       "v1-schema",
		PolicyVersion:       "v1",
		ClarificationBudget: 3,
		ComplexityScore:     0.75,
		MinQuestions:        2,
		MaxQuestions:        6,
		QuestionSequence:    []string{"q1", "q2"},
	}
	if s.GetSessionId() != "sid-1" {
		t.Errorf("GetSessionId() = %q", s.GetSessionId())
	}
	if s.GetUserId() != "uid-1" {
		t.Errorf("GetUserId() = %q", s.GetUserId())
	}
	if s.GetStatus() != pb.SessionStatus_QUESTIONING {
		t.Errorf("GetStatus() = %v", s.GetStatus())
	}
	if s.GetQuestionStopReason() != pb.QuestionStopReason_USER_PROCEED {
		t.Errorf("GetQuestionStopReason() = %v", s.GetQuestionStopReason())
	}
	if s.GetSchemaVersion() != "v1-schema" {
		t.Errorf("GetSchemaVersion() = %q", s.GetSchemaVersion())
	}
	if s.GetPolicyVersion() != "v1" {
		t.Errorf("GetPolicyVersion() = %q", s.GetPolicyVersion())
	}
	if s.GetClarificationBudget() != 3 {
		t.Errorf("GetClarificationBudget() = %d", s.GetClarificationBudget())
	}
	if s.GetMinQuestions() != 2 {
		t.Errorf("GetMinQuestions() = %d", s.GetMinQuestions())
	}
	if s.GetMaxQuestions() != 6 {
		t.Errorf("GetMaxQuestions() = %d", s.GetMaxQuestions())
	}
	if len(s.GetQuestionSequence()) != 2 {
		t.Errorf("GetQuestionSequence() length = %d", len(s.GetQuestionSequence()))
	}
	// Cover the deprecated Descriptor method.
	b, idx := s.Descriptor()
	if b == nil || len(idx) == 0 {
		t.Error("AgentSession Descriptor returned unexpected values")
	}
}
