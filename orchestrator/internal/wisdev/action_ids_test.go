package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalizeWisdevAction(t *testing.T) {
	is := assert.New(t)
	is.Equal("research.retrievePapers", CanonicalizeWisdevAction("research.searchPapers"))
	is.Equal("research.synthesizeAnswer", CanonicalizeWisdevAction("research.synthesize-answer"))
	is.Equal("research.synthesizeAnswer", CanonicalizeWisdevAction(" research.synthesize-answer "))
	is.Equal("research.evaluateEvidence", CanonicalizeWisdevAction("research.evaluate-evidence"))
	is.Equal("research.generateIdeas", CanonicalizeWisdevAction("research.generateIdeas"))
	is.Equal("unknown.action", CanonicalizeWisdevAction("unknown.action"))
}
