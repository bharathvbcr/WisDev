package wisdev

import "strings"

var legacyWisdevActionAliases = map[string]string{
	"research.searchPapers":      "research.retrievePapers",
	"research.synthesize-answer": "research.synthesizeAnswer",
	"research.evaluate-evidence": "research.evaluateEvidence",
}

const (
	ActionResearchBuildClaimEvidenceTable   = "research.buildClaimEvidenceTable"
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
	action = strings.TrimSpace(action)
	if canonical, ok := legacyWisdevActionAliases[action]; ok {
		return canonical
	}
	return action
}
