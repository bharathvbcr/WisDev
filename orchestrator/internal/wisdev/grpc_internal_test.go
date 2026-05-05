package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
	wisdevpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/wisdev"
)

func TestMapStatusToProto(t *testing.T) {
	assert.Equal(t, wisdevpb.SessionStatus_QUESTIONING, mapStatusToProto(SessionQuestioning))
	assert.Equal(t, wisdevpb.SessionStatus_COMPLETE, mapStatusToProto(SessionComplete))
	assert.Equal(t, wisdevpb.SessionStatus_SESSION_STATUS_UNSPECIFIED, mapStatusToProto(SessionStatus("unknown")))
}

func TestMapQuestionStopReasonToProto(t *testing.T) {
	assert.Equal(t, wisdevpb.QuestionStopReason_EVIDENCE_SUFFICIENT, mapQuestionStopReasonToProto(QuestionStopReasonEvidenceSufficient))
	assert.Equal(t, wisdevpb.QuestionStopReason_QUESTION_STOP_REASON_UNSPECIFIED, mapQuestionStopReasonToProto(QuestionStopReason("unknown")))
}

func TestBuildProtoSession(t *testing.T) {
	session := &AgentSession{
		SessionID:     "s1",
		UserID:        "u1",
		SchemaVersion: "v1",
		Status:        SessionQuestioning,
	}
	proto := buildProtoSession(session)
	assert.Equal(t, "s1", proto.SessionId)
	assert.Equal(t, "u1", proto.UserId)
	assert.Equal(t, wisdevpb.SessionStatus_QUESTIONING, proto.Status)
	assert.NotEmpty(t, proto.CheckpointBlob)
}

func TestMapRiskToProto(t *testing.T) {
	assert.Equal(t, wisdevpb.RiskLevel_LOW, mapRiskToProto(RiskLevelLow))
	assert.Equal(t, wisdevpb.RiskLevel_RISK_LEVEL_UNSPECIFIED, mapRiskToProto("unknown"))
}

func TestMapTargetToProto(t *testing.T) {
	assert.Equal(t, wisdevpb.ExecutionTarget_GO_NATIVE, mapTargetToProto(ExecutionTargetGoNative))
	assert.Equal(t, wisdevpb.ExecutionTarget_EXECUTION_TARGET_UNSPECIFIED, mapTargetToProto("unknown"))
}

func TestBuildQuestionStateSummaryProto(t *testing.T) {
	session := &AgentSession{
		QuestionSequence: []string{"q1", "q2"},
		Answers: map[string]Answer{
			"q1": {QuestionID: "q1", Values: []string{"a1"}},
		},
	}
	proto := buildQuestionStateSummaryProto(session)
	assert.NotNil(t, proto)
	assert.Equal(t, int32(1), proto.AnsweredCount)
}
