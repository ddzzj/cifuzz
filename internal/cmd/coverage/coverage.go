package coverage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pkg/browser"
	"github.com/pkg/errors"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"code-intelligence.com/cifuzz/internal/build/gradle"
	"code-intelligence.com/cifuzz/internal/build/maven"
	bazelCoverage "code-intelligence.com/cifuzz/internal/cmd/coverage/bazel"
	gradleCoverage "code-intelligence.com/cifuzz/internal/cmd/coverage/gradle"
	llvmCoverage "code-intelligence.com/cifuzz/internal/cmd/coverage/llvm"
	mavenCoverage "code-intelligence.com/cifuzz/internal/cmd/coverage/maven"
	"code-intelligence.com/cifuzz/internal/cmdutils"
	"code-intelligence.com/cifuzz/internal/cmdutils/resolve"
	"code-intelligence.com/cifuzz/internal/completion"
	"code-intelligence.com/cifuzz/internal/config"
	"code-intelligence.com/cifuzz/internal/coverage"
	"code-intelligence.com/cifuzz/pkg/dependencies"
	"code-intelligence.com/cifuzz/pkg/log"
	"code-intelligence.com/cifuzz/util/stringutil"
)

type Generator interface {
	BuildFuzzTestForCoverage() error
	GenerateCoverageReport() (string, error)
}

type coverageOptions struct {
	OutputFormat          string   `mapstructure:"format"`
	OutputPath            string   `mapstructure:"output"`
	BuildSystem           string   `mapstructure:"build-system"`
	BuildCommand          string   `mapstructure:"build-command"`
	CleanCommand          string   `mapstructure:"clean-command"`
	NumBuildJobs          uint     `mapstructure:"build-jobs"`
	SeedCorpusDirs        []string `mapstructure:"seed-corpus-dirs"`
	UseSandbox            bool     `mapstructure:"use-sandbox"`
	Preset                string
	ResolveSourceFilePath bool
	ProjectDir            string

	fuzzTest   string
	argsToPass []string

	buildStdout io.Writer
	buildStderr io.Writer
}

func (opts *coverageOptions) validate() error {
	var err error

	opts.SeedCorpusDirs, err = cmdutils.ValidateSeedCorpusDirs(opts.SeedCorpusDirs)
	if err != nil {
		log.Error(err, err.Error())
		return cmdutils.ErrSilent
	}

	if opts.BuildSystem == "" {
		opts.BuildSystem, err = config.DetermineBuildSystem(opts.ProjectDir)
		if err != nil {
			return err
		}
	}

	if opts.BuildSystem == config.BuildSystemNodeJS {
		err := errors.Errorf(`Starting a coverage run is currently not supported for %[1]s projects. If you
are interested in using this feature with %[1]s, please file an issue at
https://github.com/CodeIntelligenceTesting/cifuzz/issues`, cases.Title(language.Und).String(opts.BuildSystem))
		log.Print(err.Error())
		return cmdutils.WrapSilentError(err)
	}

	err = config.ValidateBuildSystem(opts.BuildSystem)
	if err != nil {
		log.Error(err)
		return cmdutils.WrapSilentError(err)
	}

	validFormats := coverage.ValidOutputFormats[opts.BuildSystem]
	if !stringutil.Contains(validFormats, opts.OutputFormat) {
		msg := fmt.Sprintf("Flag \"format\" must be %s", strings.Join(validFormats, " or "))
		return cmdutils.WrapIncorrectUsageError(errors.New(msg))
	}

	// To build with other build systems, a build command must be provided
	if opts.BuildSystem == config.BuildSystemOther && opts.BuildCommand == "" {
		msg := `Flag 'build-command' must be set when using the build system type 'other'`
		return cmdutils.WrapIncorrectUsageError(errors.New(msg))
	}

	return nil
}

type coverageCmd struct {
	*cobra.Command
	opts *coverageOptions
}

func New() *cobra.Command {
	opts := &coverageOptions{}
	var bindFlags func()

	cmd := &cobra.Command{
		Use:   "coverage [flags] <fuzz test>",
		Short: "Generate coverage report for fuzz test",
		Long: `This command generates a coverage report for a fuzz test.

The inputs found in the inputs directory of the fuzz test are used in
addition to optional input directories specified with the seed-corpus flag.
More details about the build system specific inputs directory location
can be found in the help message of the run command.

Additional arguments for CMake and Bazel can be passed after a "--".

The output can be displayed in the browser or written as a HTML
or a lcov trace file.

` + pterm.Style{pterm.Reset, pterm.Bold}.Sprint("Browser") + `
    cifuzz coverage <fuzz test>

` + pterm.Style{pterm.Reset, pterm.Bold}.Sprint("HTML") + `
    cifuzz coverage --output coverage-report <fuzz test>

` + pterm.Style{pterm.Reset, pterm.Bold}.Sprint("LCOV") + `
    cifuzz coverage --format=lcov <fuzz test>

` + pterm.Style{pterm.Reset, pterm.Bold}.Sprint("XML (Jacoco Report)") + `
    cifuzz coverage --format=jacocoxml <fuzz test>
`,
		ValidArgsFunction: completion.ValidFuzzTests,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// Bind viper keys to flags. We can't do this in the New
			// function, because that would re-bind viper keys which
			// were bound to the flags of other commands before.
			bindFlags()
			cmdutils.ViperMustBindPFlag("format", cmd.Flags().Lookup("format"))
			cmdutils.ViperMustBindPFlag("output", cmd.Flags().Lookup("output"))

			var lenFuzzTestArgs int
			var argsToPass []string
			if cmd.ArgsLenAtDash() != -1 {
				lenFuzzTestArgs = cmd.ArgsLenAtDash()
				argsToPass = args[cmd.ArgsLenAtDash():]
				args = args[:cmd.ArgsLenAtDash()]
			} else {
				lenFuzzTestArgs = len(args)
			}
			if lenFuzzTestArgs != 1 {
				msg := fmt.Sprintf("Exactly one <fuzz test> argument must be provided, got %d", lenFuzzTestArgs)
				return cmdutils.WrapIncorrectUsageError(errors.New(msg))
			}

			err := config.FindAndParseProjectConfig(opts)
			if err != nil {
				log.Errorf(err, "Failed to parse cifuzz.yaml: %v", err.Error())
				return cmdutils.WrapSilentError(err)
			}

			fuzzTest, err := resolve.FuzzTestArgument(opts.ResolveSourceFilePath, args, opts.BuildSystem, opts.ProjectDir)
			if err != nil {
				log.Error(err)
				return cmdutils.WrapSilentError(err)
			}
			opts.fuzzTest = fuzzTest[0]
			opts.argsToPass = argsToPass

			opts.buildStdout = cmd.OutOrStdout()
			opts.buildStderr = cmd.OutOrStderr()
			if cmdutils.ShouldLogBuildToFile() {
				opts.buildStdout, err = cmdutils.BuildOutputToFile(opts.ProjectDir, []string{opts.fuzzTest})
				if err != nil {
					log.Errorf(err, "Failed to setup logging: %v", err.Error())
					return cmdutils.WrapSilentError(err)
				}
				opts.buildStderr = opts.buildStdout
			}

			return opts.validate()
		},
		RunE: func(c *cobra.Command, args []string) error {
			cmd := coverageCmd{Command: c, opts: opts}
			return cmd.run()
		},
	}

	// Note: If a flag should be configurable via cifuzz.yaml as well,
	// bind it to viper in the PreRunE function.
	bindFlags = cmdutils.AddFlags(cmd,
		cmdutils.AddBuildCommandFlag,
		cmdutils.AddCleanCommandFlag,
		cmdutils.AddBuildJobsFlag,
		cmdutils.AddProjectDirFlag,
		cmdutils.AddSeedCorpusFlag,
		cmdutils.AddUseSandboxFlag,
		cmdutils.AddPresetFlag,
		cmdutils.AddResolveSourceFileFlag,
	)
	// This flag is not supposed to be called by a user
	err := cmd.Flags().MarkHidden("preset")
	if err != nil {
		panic(err)
	}
	cmd.Flags().StringP("format", "f", "html", "Output format of the coverage report (html/lcov).")
	cmd.Flags().StringP("output", "o", "", "Output path of the coverage report.")
	err = cmd.RegisterFlagCompletionFunc("format", completion.ValidCoverageOutputFormat)
	if err != nil {
		panic(err)
	}

	return cmd
}

func (c *coverageCmd) run() error {
	err := c.checkDependencies()
	if err != nil {
		return err
	}

	if cmdutils.ShouldLogBuildToFile() {
		log.CreateCurrentProgressSpinner(nil, log.BuildInProgressMsg)
	}

	log.Infof("Building %s", pterm.Style{pterm.Reset, pterm.FgLightBlue}.Sprint(c.opts.fuzzTest))

	if c.opts.Preset == "vscode" {
		var format string
		var output string
		switch c.opts.BuildSystem {
		case config.BuildSystemCMake, config.BuildSystemBazel:
			format = coverage.FormatLCOV
			output = "lcov.info"
		case config.BuildSystemMaven, config.BuildSystemGradle:
			format = coverage.FormatJacocoXML
			output = "coverage.xml"
		default:
			log.Info("The --vscode flag only supports the following build systems: CMake, Bazel, Maven, Gradle")
			return nil
		}

		c.opts.OutputFormat = format
		c.opts.OutputPath = output
	}

	var gen Generator
	switch c.opts.BuildSystem {
	case config.BuildSystemBazel:
		gen = &bazelCoverage.CoverageGenerator{
			FuzzTest:        c.opts.fuzzTest,
			OutputFormat:    c.opts.OutputFormat,
			OutputPath:      c.opts.OutputPath,
			BuildSystemArgs: c.opts.argsToPass,
			ProjectDir:      c.opts.ProjectDir,
			Engine:          "libfuzzer",
			NumJobs:         c.opts.NumBuildJobs,
			Stdout:          c.OutOrStdout(),
			Stderr:          c.ErrOrStderr(),
			BuildStdout:     c.opts.buildStdout,
			BuildStderr:     c.opts.buildStderr,
			Verbose:         viper.GetBool("verbose"),
		}
	case config.BuildSystemCMake, config.BuildSystemOther:
		if c.opts.BuildSystem == config.BuildSystemOther {
			if len(c.opts.argsToPass) > 0 {
				log.Warnf("Passing additional arguments is not supported for build system type \"other\".\n"+
					"These arguments are ignored: %s", strings.Join(c.opts.argsToPass, " "))
			}
		}

		gen = &llvmCoverage.CoverageGenerator{
			OutputFormat:    c.opts.OutputFormat,
			OutputPath:      c.opts.OutputPath,
			BuildSystem:     c.opts.BuildSystem,
			BuildCommand:    c.opts.BuildCommand,
			BuildSystemArgs: c.opts.argsToPass,
			CleanCommand:    c.opts.CleanCommand,
			NumBuildJobs:    c.opts.NumBuildJobs,
			SeedCorpusDirs:  c.opts.SeedCorpusDirs,
			UseSandbox:      c.opts.UseSandbox,
			FuzzTest:        c.opts.fuzzTest,
			ProjectDir:      c.opts.ProjectDir,
			Stderr:          c.OutOrStderr(),
			BuildStdout:     c.opts.buildStdout,
			BuildStderr:     c.opts.buildStderr,
		}
	case config.BuildSystemGradle:
		if len(c.opts.argsToPass) > 0 {
			log.Warnf("Passing additional arguments is not supported for Gradle.\n"+
				"These arguments are ignored: %s", strings.Join(c.opts.argsToPass, " "))
		}

		gen = &gradleCoverage.CoverageGenerator{
			OutputPath: c.opts.OutputPath,
			FuzzTest:   c.opts.fuzzTest,
			ProjectDir: c.opts.ProjectDir,
			Parallel: gradle.ParallelOptions{
				Enabled: viper.IsSet("build-jobs"),
			},
			Stderr:      c.OutOrStderr(),
			BuildStdout: c.opts.buildStdout,
			BuildStderr: c.opts.buildStderr,
		}
	case config.BuildSystemMaven:
		if len(c.opts.argsToPass) > 0 {
			log.Warnf("Passing additional arguments is not supported for Maven.\n"+
				"These arguments are ignored: %s", strings.Join(c.opts.argsToPass, " "))
		}

		gen = &mavenCoverage.CoverageGenerator{
			OutputPath: c.opts.OutputPath,
			FuzzTest:   c.opts.fuzzTest,
			ProjectDir: c.opts.ProjectDir,
			Parallel: maven.ParallelOptions{
				Enabled: viper.IsSet("build-jobs"),
				NumJobs: c.opts.NumBuildJobs,
			},
			Stderr:      c.OutOrStderr(),
			BuildStdout: c.opts.buildStdout,
			BuildStderr: c.opts.buildStderr,
		}
	default:
		return errors.Errorf("Unsupported build system \"%s\"", c.opts.BuildSystem)
	}

	err = gen.BuildFuzzTestForCoverage()
	if err != nil {
		if cmdutils.ShouldLogBuildToFile() {
			log.StopCurrentProgressSpinner(log.GetPtermErrorStyle(), log.BuildInProgressErrorMsg)
			printErr := cmdutils.PrintBuildLogOnStdout()
			if printErr != nil {
				log.Error(printErr)
			}
		}

		var execErr *cmdutils.ExecError
		if errors.As(err, &execErr) {
			// It is expected that some commands might fail due to user
			// configuration so we print the error without the stack trace
			// (in non-verbose mode) and silence it
			log.Error(err)
			return cmdutils.ErrSilent
		}
		return err
	}

	if cmdutils.ShouldLogBuildToFile() {
		log.StopCurrentProgressSpinner(log.GetPtermSuccessStyle(), log.BuildInProgressSuccessMsg)
		log.Info(cmdutils.GetMsgPathToBuildLog())
	}

	reportPath, err := gen.GenerateCoverageReport()
	if err != nil {
		return err
	}

	switch c.opts.OutputFormat {
	case coverage.FormatHTML:
		return c.handleHTMLReport(reportPath)
	case coverage.FormatLCOV:
		log.Successf("Created coverage lcov report: %s", reportPath)
		return nil
	case coverage.FormatJacocoXML:
		log.Successf("Created jacoco.xml coverage report: %s", reportPath)
		return nil
	default:
		return errors.Errorf("Unsupported output format")
	}
}

func (c *coverageCmd) handleHTMLReport(reportPath string) error {
	htmlFile := filepath.Join(reportPath, "index.html")

	// Open the browser if no output path was specified
	if c.opts.OutputPath == "" {
		// try to open the report in the browser ...
		err := c.openReport(htmlFile)
		if err != nil {
			//... if this fails print the file URI
			log.Debug(err)
			err = c.printReportURI(htmlFile)
			if err != nil {
				return err
			}
		}
	} else {
		log.Successf("Created coverage HTML report: %s", reportPath)
		err := c.printReportURI(htmlFile)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *coverageCmd) openReport(reportPath string) error {
	// ignore output of browser package
	browser.Stdout = io.Discard
	browser.Stderr = io.Discard
	err := browser.OpenFile(reportPath)
	return errors.WithStack(err)
}

func (c *coverageCmd) printReportURI(reportPath string) error {
	absReportPath, err := filepath.Abs(reportPath)
	if err != nil {
		return errors.WithStack(err)
	}
	reportURI := fmt.Sprintf("file://%s", filepath.ToSlash(absReportPath))
	log.Infof("To view the report, open this URI in a browser:\n\n   %s\n\n", reportURI)
	return nil
}

func (c *coverageCmd) checkDependencies() error {
	var deps []dependencies.Key
	switch c.opts.BuildSystem {
	case config.BuildSystemBazel:
		deps = []dependencies.Key{
			dependencies.Bazel,
			dependencies.GenHTML,
		}
	case config.BuildSystemCMake:
		deps = []dependencies.Key{
			dependencies.CMake,
			dependencies.LLVMSymbolizer,
			dependencies.LLVMCov,
			dependencies.LLVMProfData,
			dependencies.GenHTML,
		}
		switch runtime.GOOS {
		case "linux", "darwin":
			deps = append(deps, dependencies.Clang)
		case "windows":
			deps = append(deps, dependencies.VisualStudio, dependencies.Perl)
		}
	case config.BuildSystemMaven:
		deps = []dependencies.Key{
			dependencies.Maven,
		}
	case config.BuildSystemGradle:
		// First check if gradle wrapper exists and check for gradle in path otherwise
		wrapper, err := gradle.FindGradleWrapper(c.opts.ProjectDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if wrapper != "" {
			return nil
		}

		deps = []dependencies.Key{
			dependencies.Gradle,
		}
	case config.BuildSystemOther:
		deps = []dependencies.Key{
			dependencies.Clang,
			dependencies.LLVMSymbolizer,
			dependencies.LLVMCov,
			dependencies.LLVMProfData,
			dependencies.GenHTML,
		}
	default:
		return errors.Errorf("Unsupported build system \"%s\"", c.opts.BuildSystem)
	}
	err := dependencies.Check(deps)
	if err != nil {
		log.Error(err)
		return cmdutils.WrapSilentError(err)
	}
	return nil
}
