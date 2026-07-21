package wisdev

import "strings"

const (
	ActionResearchBuildClaimEvidenceTable   = "research.buildClaimEvidenceTable"
	ActionResearchDetectContradictions      = "research.detectContradictions"
	ActionResearchEvaluateEvidence          = "research.evaluateEvidence"
	ActionResearchFullPaperGatewayDispatch  = "research.fullPaperGatewayDispatch"
	ActionResearchFullPaperRetrieve         = "research.fullPaperRetrieve"
	ActionResearchGenerateHypotheses        = "research.generateHypotheses"
	ActionResearchGenerateIdeas             = "research.generateIdeas"
	ActionResearchGenerateThoughts          = "research.generateThoughts"
	ActionResearchProposeHypotheses         = "research.proposeHypotheses"
	ActionResearchQueryDecompose            = "research.queryDecompose"
	ActionResearchResolveCanonicalCitations = "research.resolveCanonicalCitations"
	ActionResearchRetrievePapers            = "research.retrievePapers"
	ActionResearchSynthesizeAnswer          = "research.synthesizeAnswer"
	ActionResearchVerifyCitations           = "research.verifyCitations"
	ActionResearchVerifyClaimsBatch         = "research.verifyClaimsBatch"
	ActionResearchVerifyReasoningPaths      = "research.verifyReasoningPaths"
)

func CanonicalizeWisdevAction(action string) string {
	return strings.TrimSpace(action)
}
