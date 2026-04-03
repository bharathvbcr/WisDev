package wisdev

import (
	legacy "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/v2"

	grpc "google.golang.org/grpc"
)

type (
	SessionStatus                    = legacy.SessionStatus
	QuestionStopReason               = legacy.QuestionStopReason
	RiskLevel                        = legacy.RiskLevel
	ExecutionTarget                  = legacy.ExecutionTarget
	AgentSession                     = legacy.AgentSession
	QuestionStateSummary             = legacy.QuestionStateSummary
	PlanExecutionUpdate              = legacy.PlanExecutionUpdate
	StepStarted                      = legacy.StepStarted
	StepCompleted                    = legacy.StepCompleted
	StepFailed                       = legacy.StepFailed
	PlanRevised                      = legacy.PlanRevised
	PaperFound                       = legacy.PaperFound
	Progress                         = legacy.Progress
	ConfirmationRequired             = legacy.ConfirmationRequired
	CreateSessionRequest             = legacy.CreateSessionRequest
	CreateSessionResponse            = legacy.CreateSessionResponse
	GetSessionRequest                = legacy.GetSessionRequest
	GetSessionResponse               = legacy.GetSessionResponse
	ResumeSessionRequest             = legacy.ResumeSessionRequest
	ResumeSessionResponse            = legacy.ResumeSessionResponse
	GetNextQuestionRequest           = legacy.GetNextQuestionRequest
	GetNextQuestionResponse          = legacy.GetNextQuestionResponse
	SubmitAnswerRequest              = legacy.SubmitAnswerRequest
	SubmitAnswerResponse             = legacy.SubmitAnswerResponse
	ExecutePlanRequest               = legacy.ExecutePlanRequest
	CancelPlanRequest                = legacy.CancelPlanRequest
	CancelPlanResponse               = legacy.CancelPlanResponse
	ListToolsRequest                 = legacy.ListToolsRequest
	ListToolsResponse                = legacy.ListToolsResponse
	ToolDefinition                   = legacy.ToolDefinition
	InvokeToolRequest                = legacy.InvokeToolRequest
	InvokeToolResponse               = legacy.InvokeToolResponse
	SaveCheckpointRequest            = legacy.SaveCheckpointRequest
	SaveCheckpointResponse           = legacy.SaveCheckpointResponse
	LoadCheckpointRequest            = legacy.LoadCheckpointRequest
	LoadCheckpointResponse           = legacy.LoadCheckpointResponse
	ExecutionError                   = legacy.ExecutionError
	ToolSearchRequest                = legacy.ToolSearchRequest
	ToolSearchResult                 = legacy.ToolSearchResult
	ToolSearchResponse               = legacy.ToolSearchResponse
	GuardrailDecision                = legacy.GuardrailDecision
	ProgrammaticLoopRequest          = legacy.ProgrammaticLoopRequest
	ProgrammaticLoopIteration        = legacy.ProgrammaticLoopIteration
	ProgrammaticLoopResponse         = legacy.ProgrammaticLoopResponse
	StructuredOutputRequest          = legacy.StructuredOutputRequest
	StructuredOutputResponse         = legacy.StructuredOutputResponse
	SearchRequest                    = legacy.SearchRequest
	SearchResponse                   = legacy.SearchResponse
	SearchUpdate                     = legacy.SearchUpdate
	SearchPaper                      = legacy.SearchPaper
	IterationLog                     = legacy.IterationLog
	IterativeSearchRequest           = legacy.IterativeSearchRequest
	IterativeSearchResponse          = legacy.IterativeSearchResponse
	ReRankRequest                    = legacy.ReRankRequest
	ReRankDocument                   = legacy.ReRankDocument
	ReRankResponse                   = legacy.ReRankResponse
	AgentGatewayServer               = legacy.AgentGatewayServer
	AgentGateway_ExecutePlanServer   = legacy.AgentGateway_ExecutePlanServer
	SearchGatewayServer              = legacy.SearchGatewayServer
	SearchGateway_StreamSearchServer = legacy.SearchGateway_StreamSearchServer
	UnimplementedAgentGatewayServer  = legacy.UnimplementedAgentGatewayServer
	UnimplementedSearchGatewayServer = legacy.UnimplementedSearchGatewayServer
)

const (
	SessionStatus_SESSION_STATUS_UNSPECIFIED = legacy.SessionStatus_SESSION_STATUS_UNSPECIFIED
	SessionStatus_QUESTIONING                = legacy.SessionStatus_QUESTIONING
	SessionStatus_GENERATING_TREE            = legacy.SessionStatus_GENERATING_TREE
	SessionStatus_EDITING_TREE               = legacy.SessionStatus_EDITING_TREE
	SessionStatus_EXECUTING_PLAN             = legacy.SessionStatus_EXECUTING_PLAN
	SessionStatus_PAUSED                     = legacy.SessionStatus_PAUSED
	SessionStatus_COMPLETE                   = legacy.SessionStatus_COMPLETE
	SessionStatus_FAILED                     = legacy.SessionStatus_FAILED

	QuestionStopReason_QUESTION_STOP_REASON_UNSPECIFIED = legacy.QuestionStopReason_QUESTION_STOP_REASON_UNSPECIFIED
	QuestionStopReason_EVIDENCE_SUFFICIENT              = legacy.QuestionStopReason_EVIDENCE_SUFFICIENT
	QuestionStopReason_CLARIFICATION_BUDGET_REACHED     = legacy.QuestionStopReason_CLARIFICATION_BUDGET_REACHED
	QuestionStopReason_USER_PROCEED                     = legacy.QuestionStopReason_USER_PROCEED

	RiskLevel_RISK_LEVEL_UNSPECIFIED = legacy.RiskLevel_RISK_LEVEL_UNSPECIFIED
	RiskLevel_LOW                    = legacy.RiskLevel_LOW
	RiskLevel_MEDIUM                 = legacy.RiskLevel_MEDIUM
	RiskLevel_HIGH                   = legacy.RiskLevel_HIGH

	ExecutionTarget_EXECUTION_TARGET_UNSPECIFIED = legacy.ExecutionTarget_EXECUTION_TARGET_UNSPECIFIED
	ExecutionTarget_GO_NATIVE                    = legacy.ExecutionTarget_GO_NATIVE
	ExecutionTarget_PYTHON_CAPABILITY            = legacy.ExecutionTarget_PYTHON_CAPABILITY
	ExecutionTarget_PYTHON_SANDBOX               = legacy.ExecutionTarget_PYTHON_SANDBOX
)

func RegisterAgentGatewayServer(s grpc.ServiceRegistrar, srv AgentGatewayServer) {
	legacy.RegisterAgentGatewayServer(s, srv)
}

func RegisterSearchGatewayServer(s grpc.ServiceRegistrar, srv SearchGatewayServer) {
	legacy.RegisterSearchGatewayServer(s, srv)
}
