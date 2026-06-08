package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIntValueAndAsFloat(t *testing.T) {
	t.Run("int variants", func(t *testing.T) {
		assert.Equal(t, 7, IntValue(7))
		assert.Equal(t, 7, IntValue(int32(7)))
		assert.Equal(t, 7, IntValue(int64(7)))
		assert.Equal(t, 7, IntValue(7.9))
		assert.Equal(t, 0, IntValue("7"))

		assert.Equal(t, int64(9), IntValue64(int64(9)))
		assert.Equal(t, int64(9), IntValue64(int32(9)))
		assert.Equal(t, int64(9), IntValue64(9.4))
		assert.Equal(t, int64(0), IntValue64("9"))
	})

	t.Run("float variants", func(t *testing.T) {
		assert.Equal(t, 3.5, AsFloat(3.5))
		assert.Equal(t, 3.0, AsFloat(3))
		assert.Equal(t, 3.0, AsFloat(int32(3)))
		assert.Equal(t, 3.0, AsFloat(int64(3)))
		assert.Equal(t, 0.0, AsFloat("3"))
	})
}
