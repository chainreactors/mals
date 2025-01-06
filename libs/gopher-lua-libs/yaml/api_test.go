package yaml

import (
	"github.com/chainreactors/mals/libs/gopher-lua-libs/strings"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/tests"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestApi(t *testing.T) {
	preload := tests.SeveralPreloadFuncs(
		Preload,
		strings.Preload,
	)
	assert.NotZero(t, tests.RunLuaTestFile(t, preload, "./test/test_api.lua"))
}
