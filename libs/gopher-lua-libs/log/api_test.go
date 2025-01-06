package log

import (
	"github.com/chainreactors/mals/libs/gopher-lua-libs/filepath"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/strings"
	"github.com/chainreactors/mals/libs/gopher-lua-libs/tests"
	"github.com/stretchr/testify/assert"
	"testing"

	ioutil "github.com/chainreactors/mals/libs/gopher-lua-libs/ioutil"
)

func TestApi(t *testing.T) {
	preload := tests.SeveralPreloadFuncs(
		ioutil.Preload,
		Preload,
	)
	assert.NotZero(t, tests.RunLuaTestFile(t, preload, "./test/test_api.lua"))
}

func TestLogLevelApi(t *testing.T) {
	preload := tests.SeveralPreloadFuncs(
		ioutil.Preload,
		filepath.Preload,
		strings.Preload,
		Preload,
	)
	assert.NotZero(t, tests.RunLuaTestFile(t, preload, "./test/test_loglevel.lua"))
}
