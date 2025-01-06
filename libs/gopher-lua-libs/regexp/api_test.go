package regexp

import (
	"github.com/chainreactors/mals/libs/gopher-lua-libs/tests"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestApi(t *testing.T) {
	assert.NotZero(t, tests.RunLuaTestFile(t, Preload, "./test/test_api.lua"))
}
