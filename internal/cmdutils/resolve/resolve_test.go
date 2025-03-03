package resolve

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"code-intelligence.com/cifuzz/internal/config"
)

func TestResolve(t *testing.T) {
	originalWd, err := os.Getwd()
	require.NoError(t, err)
	revertToOriginalWd := func() {
		err = os.Chdir(originalWd)
		require.NoError(t, err)
	}

	changeWdToTestData := func(dir string) string {
		err := os.Chdir(filepath.Join("testdata", dir))
		require.NoError(t, err)
		pwd, err := os.Getwd()
		require.NoError(t, err)
		return pwd
	}

	t.Run("resolveBazel", func(t *testing.T) {
		defer revertToOriginalWd()
		pwd := changeWdToTestData("bazel")
		testResolveBazel(t, pwd)
	})

	t.Run("resolveCmake", func(t *testing.T) {
		defer revertToOriginalWd()
		pwd := changeWdToTestData("cmake")
		testResolveCMake(t, pwd)
	})

	t.Run("testResolveMavenGradle", func(t *testing.T) {
		defer revertToOriginalWd()
		pwd := changeWdToTestData("maven_gradle")
		testResolveMavenGradle(t, pwd)
	})

	t.Run("testResolveMavenGradleWindowsPaths", func(t *testing.T) {
		defer revertToOriginalWd()
		pwd := changeWdToTestData("maven_gradle")
		testResolveMavenGradleWindowsPaths(t, pwd)
	})
}

func testResolveBazel(t *testing.T, pwd string) {
	fuzzTestName := "//src/fuzz_test_1:fuzz_test_1"

	// relative path
	srcFile := filepath.Join("src", "fuzz_test_1", "fuzz_test.cpp")
	resolved, err := resolve(srcFile, config.BuildSystemBazel, pwd)
	require.NoError(t, err)
	require.Equal(t, fuzzTestName, resolved)

	// absolute path
	srcFile = filepath.Join(pwd, srcFile)
	resolved, err = resolve(srcFile, config.BuildSystemBazel, pwd)
	require.NoError(t, err)
	require.Equal(t, fuzzTestName, resolved)
}

func testResolveCMake(t *testing.T, pwd string) {
	fuzzTestName := "fuzz_test_1"

	// relative path
	srcFile := filepath.Join("src", "fuzz_test_1", "fuzz_test.cpp")
	resolved, err := resolve(srcFile, config.BuildSystemCMake, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)

	// absolute path
	srcFile = filepath.Join(pwd, srcFile)
	resolved, err = resolve(srcFile, config.BuildSystemCMake, pwd)
	require.NoError(t, err)
	require.Equal(t, fuzzTestName, resolved)
}

func testResolveMavenGradle(t *testing.T, pwd string) {
	fuzzTestName := "com.example.fuzz_test_1.FuzzTestCase"

	// Java file
	// relative path
	srcFile := filepath.Join("src", "test", "java", "com", "example", "fuzz_test_1", "FuzzTestCase.java")
	resolved, err := resolve(srcFile, config.BuildSystemGradle, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)
	resolved, err = resolve(srcFile, config.BuildSystemMaven, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)

	// absolute path
	srcFile = filepath.Join(pwd, srcFile)
	resolved, err = resolve(srcFile, config.BuildSystemGradle, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)
	resolved, err = resolve(srcFile, config.BuildSystemMaven, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)

	// Kotlin file
	// relative path
	srcFile = filepath.Join("src", "test", "kotlin", "com", "example", "fuzz_test_1", "FuzzTestCase.kt")
	resolved, err = resolve(srcFile, config.BuildSystemGradle, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)
	resolved, err = resolve(srcFile, config.BuildSystemMaven, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)

	// absolute path
	srcFile = filepath.Join(pwd, srcFile)
	resolved, err = resolve(srcFile, config.BuildSystemGradle, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)
	resolved, err = resolve(srcFile, config.BuildSystemMaven, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)
}

func testResolveMavenGradleWindowsPaths(t *testing.T, pwd string) {
	if runtime.GOOS != "windows" {
		t.Skip()
	}

	fuzzTestName := "com.example.fuzz_test_1.FuzzTestCase"

	srcFile := "src/test/java/com/example/fuzz_test_1/FuzzTestCase.java"
	resolved, err := resolve(srcFile, config.BuildSystemGradle, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)
	resolved, err = resolve(srcFile, config.BuildSystemMaven, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)

	srcFile = "src\\test\\java\\com\\example\\fuzz_test_1\\FuzzTestCase.java"
	resolved, err = resolve(srcFile, config.BuildSystemGradle, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)
	resolved, err = resolve(srcFile, config.BuildSystemMaven, pwd)
	assert.NoError(t, err)
	assert.Equal(t, fuzzTestName, resolved)
}
