package dependencies

import (
	"errors"
	"fmt"

	"github.com/Masterminds/semver"

	"code-intelligence.com/cifuzz/pkg/log"
	"code-intelligence.com/cifuzz/pkg/runfiles"
)

var ErrDeps = errors.New(`Unable to run command due to missing/invalid dependencies.
For installation instruction see:

	https://github.com/CodeIntelligenceTesting/cifuzz#installation

`)

type Key string

const (
	Bazel          Key = "bazel"
	Clang          Key = "clang"
	CMake          Key = "cmake"
	LLVMCov        Key = "llvm-cov"
	LLVMSymbolizer Key = "llvm-symbolizer"
	LLVMProfData   Key = "llvm-profdata"

	GenHTML Key = "genhtml"
	Perl    Key = "perl"

	Java   Key = "java"
	Maven  Key = "mvn"
	Gradle Key = "gradle"

	VisualStudio Key = "Visual Studio"

	MessageVersion = "cifuzz requires %s %s or higher, have %s"
	MessageMissing = "cifuzz requires %s, but it is not installed"
)

// Dependency represents a single dependency
type Dependency struct {
	finder runfiles.RunfilesFinder

	Key        Key
	MinVersion semver.Version
	// these fields are used to implement custom logic to
	// retrieve version or installation information for the
	// specific dependency
	GetVersion func(*Dependency) (*semver.Version, error)
	Installed  func(*Dependency) bool
}

// Compares MinVersion against GetVersion
func (dep *Dependency) checkVersion() bool {
	currentVersion, err := dep.GetVersion(dep)
	if err != nil {
		log.Warnf("Unable to get current version for %s, message: %v", dep.Key, err)
		// we want to be lenient if we were not able to extract the version
		return true
	}

	if currentVersion.Compare(&dep.MinVersion) == -1 {
		log.Warnf(MessageVersion, dep.Key, dep.MinVersion.String(), currentVersion.String())
		return false
	}
	return true
}

// helper to easily check against functions from the runfiles.RunfilesFinder interface
func (dep *Dependency) checkFinder(finderFunc func() (string, error)) bool {
	if _, err := finderFunc(); err != nil {
		return false
	}
	return true
}

// Check iterates of a list of dependencies and checks if they are fulfilled
func Check(keys []Key) error {
	return check(keys, deps, runfiles.Finder)
}

func check(keys []Key, deps Dependencies, finder runfiles.RunfilesFinder) error {
	allFine := true
	for _, key := range keys {
		dep, found := deps[key]
		if !found {
			panic(fmt.Sprintf("Undefined dependency %s", key))
		}

		dep.finder = finder

		if !dep.Installed(dep) {
			log.Warnf(MessageMissing, dep.Key)
			allFine = false
			continue
		}

		if dep.MinVersion.Equal(semver.MustParse("0.0.0")) {
			log.Debugf("Checking dependency: %s ", dep.Key)
		} else {
			log.Debugf("Checking dependency: %s version >= %s", dep.Key, dep.MinVersion.String())
		}

		if !dep.checkVersion() {
			allFine = false
		}

	}

	if !allFine {
		return ErrDeps
	}
	return nil
}
