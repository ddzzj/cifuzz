package shared

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"code-intelligence.com/cifuzz/integration-tests/shared/mockserver"
	"code-intelligence.com/cifuzz/util/envutil"
	"code-intelligence.com/cifuzz/util/executil"
	"code-intelligence.com/cifuzz/util/fileutil"
)

func TestRunWithUpload(t *testing.T, dir string, cifuzz string, args ...string) {
	projectName := "my_fuzz_test-bac40407"

	server := mockserver.New(t)

	// define handlers
	server.Handlers["/v1/projects"] = mockserver.ReturnResponse(t, mockserver.ProjectsJSON)
	server.Handlers["/v2/error-details"] = mockserver.ReturnResponse(t, mockserver.ProjectsJSON)
	server.Handlers[fmt.Sprintf("/v1/projects/%s/campaign_runs", projectName)] = mockserver.ReturnResponse(t, "{}")
	server.Handlers[fmt.Sprintf("/v1/projects/%s/findings", projectName)] = mockserver.ReturnResponse(t, "{}")

	// start the server
	server.Start(t)

	tempDir, err := os.MkdirTemp("", "cifuzz-run-*")
	require.NoError(t, err)
	defer fileutil.Cleanup(tempDir)

	// Create a dictionary
	dictPath := filepath.Join(tempDir, "some_dict")
	err = os.WriteFile(dictPath, []byte("test-dictionary-content"), 0o600)
	require.NoError(t, err)

	// Create a seed corpus directory with an empty seed
	seedCorpusDir, err := os.MkdirTemp(tempDir, "seeds-")
	require.NoError(t, err)
	err = fileutil.Touch(filepath.Join(seedCorpusDir, "empty"))
	require.NoError(t, err)

	args = append(
		[]string{
			"run",
			"--project", projectName,
			"--server", server.Address,
			"--interactive=false",
			"--no-notifications",
			"crashing_fuzz_test",
		}, args...)

	cmd := executil.Command(cifuzz, args...)
	cmd.Dir = dir

	cmd.Env, err = envutil.Setenv(os.Environ(), "CIFUZZ_API_TOKEN", "test-token")
	require.NoError(t, err)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err)

	assert.Contains(t, string(out), "✓ You are authenticated.")
	assert.Contains(t, string(out), "You can view the findings at http://127.0.0.1")
}
