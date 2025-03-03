package shared

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/alexflint/go-filemutex"
	"github.com/otiai10/copy"
	"github.com/stretchr/testify/require"

	builderPkg "code-intelligence.com/cifuzz/internal/builder"
	"code-intelligence.com/cifuzz/pkg/finding"
	"code-intelligence.com/cifuzz/util/executil"
	"code-intelligence.com/cifuzz/util/fileutil"
)

var installOnce sync.Once
var installMutex *filemutex.FileMutex
var installDir string

// AddLinesToFileAtBreakPoint adds the given lines before or after the breakpoint
// to the file at the given path.
func AddLinesToFileAtBreakPoint(t *testing.T, filePath string, linesToAdd []string, breakPoint string, addAfterBreakpoint bool) {
	t.Helper()

	f, err := os.OpenFile(filePath, os.O_RDWR, 0700)
	require.NoError(t, err)
	defer f.Close()

	// Add lines to pom.xml
	scanner := bufio.NewScanner(f)
	var lines []string
	var addedLines bool
	for scanner.Scan() {
		if !addedLines && strings.HasPrefix(scanner.Text(), breakPoint) {
			if addAfterBreakpoint {
				lines = append(lines, scanner.Text())
				lines = append(lines, linesToAdd...)
				addedLines = true
				continue
			}

			lines = append(lines, linesToAdd...)
			addedLines = true
		}
		lines = append(lines, scanner.Text())
	}
	if !addedLines {
		require.FailNow(t, fmt.Sprintf("couldn't find breakpoint %s line in %s", breakPoint, filePath))
	}

	// Write the new content of pom.xml back to filePath
	_, err = f.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	require.NoError(t, err)
}

func AppendLines(t *testing.T, filePath string, linesToAdd []string) {
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	require.NoError(t, err)
	defer f.Close()

	_, err = f.WriteString(strings.Join(linesToAdd, "\n") + "\n")
	require.NoError(t, err)
}

// Used for e2e tests
// CopyTestdataDirForE2E copies a named folder from the samples directory
// to a temporary directory called "cifuzz-<name>-testdata" and returns the path.
func CopyTestdataDirForE2E(t *testing.T, name string) string {
	t.Helper()
	fileutil.ForceLongPathTempDir()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	dir, err := os.MkdirTemp("", fmt.Sprintf("cifuzz-%s-testdata-", name))
	require.NoError(t, err)

	// Get the path to the testdata dir
	testDataDir := filepath.Join(cwd, "..", "samples", name)

	// Copy the testdata dir to the temporary directory
	err = copy.Copy(testDataDir, dir)
	require.NoError(t, err)

	return dir
}

// CopyTestdataDir copies the "testdata" folder in the current working directory
// to a temporary directory called "cifuzz-<name>-testdata" and returns the path.
func CopyTestdataDir(t *testing.T, name string) string {
	fileutil.ForceLongPathTempDir()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	dir, err := os.MkdirTemp("", fmt.Sprintf("cifuzz-%s-testdata-", name))
	require.NoError(t, err)

	dir, err = filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	// Get the path to the testdata dir
	testDataDir := filepath.Join(cwd, "testdata")

	// Copy the testdata dir to the temporary directory
	err = copy.Copy(testDataDir, dir)
	require.NoError(t, err)

	return dir
}

func GetFindings(t *testing.T, cifuzz string, dir string) []*finding.Finding {
	cmd := executil.Command(cifuzz, "findings", "--json", "--interactive=false")
	cmd.Dir = dir
	output, err := cmd.Output()
	require.NoError(t, err)

	var findings []*finding.Finding
	err = json.Unmarshal(output, &findings)
	require.NoError(t, err)
	return findings
}

// InstallCIFuzzInTemp creates an installation builder and extracts it
// into a temporary directory. The caller should clean up the returned
// directory.
func InstallCIFuzzInTemp(t *testing.T) string {
	t.Helper()

	var err error
	lockFile := filepath.Join(os.TempDir(), ".cifuzz-build-lock")
	installMutex, err = filemutex.New(lockFile)
	require.NoError(t, err)

	err = installMutex.Lock()

	defer func() {
		err = installMutex.Unlock()
		require.NoError(t, err)
	}()

	installOnce.Do(func() {
		// Create directory for installation files
		installDir, err = os.MkdirTemp("", "cifuzz-")
		require.NoError(t, err)

		// Build cifuzz in the install directory
		opts := builderPkg.Options{Version: "dev", TargetDir: installDir}
		builder, err := builderPkg.NewCIFuzzBuilder(opts)
		require.NoError(t, err)
		err = builder.BuildCIFuzzAndDeps()
		require.NoError(t, err)
	})

	return installDir
}

func ModifyFuzzTestToCallFunction(t *testing.T, fuzzTestPath string) {
	// Modify the fuzz test stub created by `cifuzz create` to actually
	// call a function.

	f, err := os.OpenFile(fuzzTestPath, os.O_RDWR, 0700)
	require.NoError(t, err)
	defer f.Close()
	scanner := bufio.NewScanner(f)
	// At the top of the file we add the required headers
	lines := []string{`#include "parser.h"`}
	var seenBeginningOfFuzzTestFunc bool
	var addedFunctionCall bool
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "FUZZ_TEST(") {
			seenBeginningOfFuzzTestFunc = true
		}
		// Insert the function call at the end of the FUZZ_TEST
		// function, right above the "}".
		if seenBeginningOfFuzzTestFunc && strings.HasPrefix(scanner.Text(), "}") {
			lines = append(lines, "  parse(std::string(reinterpret_cast<const char *>(data), size));")
			addedFunctionCall = true
		}
		lines = append(lines, scanner.Text())
	}
	require.NoError(t, scanner.Err())
	require.True(t, addedFunctionCall)

	// Write the new content of the fuzz test back to file
	_, err = f.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, err = f.WriteString(strings.Join(lines, "\n"))
	require.NoError(t, err)
}

func TerminateOnSignal(t *testing.T, cmd *executil.Cmd) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	go func() {
		s := <-sigs
		t.Logf("Received %s", s.String())

		// Re-raise the signal for other handlers
		signal.Stop(sigs)
		p, err := os.FindProcess(os.Getpid())
		require.NoError(t, err)
		err = p.Signal(s)
		require.NoError(t, err)

		// Terminate the command's process group
		err = cmd.TerminateProcessGroup()
		require.NoError(t, err)
	}()
}
