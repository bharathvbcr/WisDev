package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalizeWisdevAction(t *testing.T) {
	is := assert.New(t)
	is.Equal("research.synthesizeAnswer", CanonicalizeWisdevAction(" research.synthesizeAnswer "))
	is.Equal("research.generateIdeas", CanonicalizeWisdevAction("research.generateIdeas"))
	is.Equal("unknown.action", CanonicalizeWisdevAction("unknown.action"))
}
