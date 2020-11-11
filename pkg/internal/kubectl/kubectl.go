/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

//Package kubectl executes various kubectl sub-commands in a forked shell
//go:generate mockgen -destination=../mocks/kubectl/mockKubectl.go -package=mocks . Kubectl,Command
package kubectl

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	"github.com/fidelity/kraan/pkg/utils"
)

const (
	kustomizeYaml = "kustomization.yaml"
)

var (
	kubectlCmd          = "kubectl"
	kustomizeCmd        = "kustomize"
	applyArgs           = []string{"-R", "-f"}
	newExecProviderFunc = newExecProvider
	tempDirProviderFunc = createTempDir
)

// Kubectl is a Factory interface that returns concrete Command implementations from named constructors.
type Kubectl interface {
	Apply(path string) (c Command)
	Delete(args ...string) (c Command)
	Get(args ...string) (c Command)
}

// Kustomize is a Factory interface that returns concrete Command implementations from named constructors.
type Kustomize interface {
	Build(path string) (c Command)
}

// NewKubectl returns a Kubectl object for creating and running kubectl sub-commands.
func NewKubectl(logger logr.Logger) (kubectl Kubectl, err error) {
	execProvider := newExecProviderFunc()
	return newCommandFactory(logger, execProvider, kubectlCmd)
}

// NewKustomize returns a Kustomize object for creating and running Kustomize sub-commands.
func NewKustomize(logger logr.Logger) (kustomize Kustomize, err error) {
	execProvider := newExecProviderFunc()
	return newCommandFactory(logger, execProvider, kustomizeCmd)
}

// CommandFactory is a concrete Factory implementation of the Kubectl interface's API.
type CommandFactory struct {
	logger       logr.Logger
	path         string
	execProvider ExecProvider
}

func newCommandFactory(logger logr.Logger, execProvider ExecProvider, execProg string) (factory *CommandFactory, err error) {
	factory = &CommandFactory{
		logger:       logger,
		execProvider: execProvider,
	}
	factory.path, err = factory.getExecProvider().FindOnPath(execProg)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to find %s binary on system PATH", execProg)
	}
	return factory, nil
}

func (f CommandFactory) getLogger() logr.Logger {
	return f.logger
}

func (f CommandFactory) getPath() string {
	return f.path
}

func (f CommandFactory) getExecProvider() ExecProvider {
	return f.execProvider
}

// Command defines an interface for commands created by the Kubectl factory type.
type Command interface {
	Run() (output []byte, err error)
	Build() (buildDir string)
	DryRun() (output []byte, err error)
	WithLogger(logger logr.Logger) (self Command)
	getPath() string
	getSubCmd() string
	getArgs() []string
	asString() string
	isJSONOutput() bool
}

// abstractCommand is a parent type with common logic and fields used by concrete Command types.
type abstractCommand struct {
	logger     logr.Logger
	factory    *CommandFactory
	subCmd     string
	jsonOutput bool
	args       []string
	cmd        string
	output     []byte
}

func (c *abstractCommand) logDebug(msg string, keysAndValues ...interface{}) {
	keysAndValues = append(keysAndValues, utils.GetFunctionAndSource(utils.MyCaller+1)...)
	c.logger.V(1).Info(msg, append(keysAndValues, "command", c.asString())...)
}

func (c *abstractCommand) logError(sourceErr error, keysAndValues ...interface{}) (err error) {
	msg := "error executing kubectl command"
	c.logger.Error(err, msg, append(utils.GetFunctionAndSource(utils.MyCaller+1), keysAndValues, "command", c.asString())...)
	return fmt.Errorf("%s '%s' : %w", msg, c.asString(), sourceErr)
}

func (c *abstractCommand) getPath() (path string) {
	return c.factory.path
}

func (c *abstractCommand) getSubCmd() (subCmd string) {
	return c.subCmd
}

func (c *abstractCommand) getArgs() (kargz []string) {
	return append([]string{c.subCmd}, c.args...)
}

func (c *abstractCommand) isJSONOutput() bool {
	return c.jsonOutput
}

func (c *abstractCommand) asString() (cmdString string) {
	if c.cmd == "" {
		c.cmd = strings.Join(append([]string{c.getPath()}, c.getArgs()...), " ")
	}
	return c.cmd
}

// Run executes the Kubectl command with all its arguments and returns the output.
func (c *abstractCommand) Run() (output []byte, err error) {
	utils.TraceCall(c.logger)
	defer utils.TraceExit(c.logger)
	if c.jsonOutput {
		c.args = append(c.args, "-o", "json")
	}
	c.logDebug("executing kubectl")
	c.output, err = c.factory.getExecProvider().ExecCmd(c.getPath(), c.getArgs()...)
	if err != nil {
		return nil, errors.WithMessage(err, "failed to execute kubectl")
	}
	return c.output, nil
}

// createTempDir creates a temporary directory.
func createTempDir() (buildDir string, err error) {
	buildDir, err = ioutil.TempDir("", "build-*")
	if err != nil {
		return "", errors.WithMessage(err, "failed to create temporary directory")
	}
	return buildDir, nil
}

// Build executes the Kustomize command with all its arguments and returns the output.
func (c *abstractCommand) Build() (buildDir string) {
	utils.TraceCall(c.logger)
	defer utils.TraceExit(c.logger)
	var err error
	buildDir, err = tempDirProviderFunc()
	if err != nil {
		c.logError(err) // nolint:errcheck //ok
		return buildDir
	}
	c.args = append(c.args, "-o", buildDir)
	c.logDebug("executing kustomize build")
	c.output, err = c.factory.getExecProvider().ExecCmd(c.getPath(), c.getArgs()...)
	if err != nil {
		c.logError(err) // nolint:errcheck //ok
	}
	return buildDir
}

// DryRun executes the Kubectl command as a dry run and returns the output without making any changes to the cluster.
func (c *abstractCommand) DryRun() (output []byte, err error) {
	c.args = append(c.args, "--server-dry-run")
	return c.Run()
}

// WithLogger sets the Logger the command should use to log actions if passed a Logger that is not nil.
func (c *abstractCommand) WithLogger(logger logr.Logger) (self Command) {
	if logger != nil {
		c.logger = logger
	}
	return c
}

// ApplyCommand is a Kubectl sub-command that recursively applies all the YAML files it finds in a directory.
type ApplyCommand struct {
	abstractCommand
}

// BuildCommand is a kustomize sub-command that processes a kustomization.yaml file.
type BuildCommand struct {
	abstractCommand
}

func (f *CommandFactory) kustomizationBuiler(path string, log logr.Logger) string {
	utils.TraceCall(f.logger)
	defer utils.TraceExit(f.logger)
	kustomize, err := NewKustomize(log)
	if err != nil {
		log.Error(err, "failed to create kustomize command object", utils.GetFunctionAndSource(utils.MyCaller)...)
		return path
	}
	return kustomize.Build(path).Build()
}

// Build instantiates an BuildCommand instance using the provided directory path.
func (f *CommandFactory) Build(path string) (c Command) {
	utils.TraceCall(f.logger)
	defer utils.TraceExit(f.logger)
	c = &BuildCommand{
		abstractCommand: abstractCommand{
			logger:     f.logger,
			factory:    f,
			subCmd:     "build",
			jsonOutput: true,
			args:       []string{path},
		},
	}
	return c
}

// Apply instantiates an ApplyCommand instance using the provided directory path.
func (f *CommandFactory) Apply(path string) (c Command) {
	utils.TraceCall(f.logger)
	defer utils.TraceExit(f.logger)
	if f.isKustomization(path) {
		path = f.kustomizationBuiler(path, f.logger)
	}
	c = &ApplyCommand{
		abstractCommand: abstractCommand{
			logger:     f.logger,
			factory:    f,
			subCmd:     "apply",
			jsonOutput: true,
			args:       append(applyArgs, path),
		},
	}
	return c
}

func (f *CommandFactory) isKustomization(dir string) bool {
	utils.TraceCall(f.logger)
	defer utils.TraceExit(f.logger)
	return f.getExecProvider().FileExists(filepath.Join(dir, kustomizeYaml))
}

// DeleteCommand implements the Command interface to delete resources from the KubeAPI service.
type DeleteCommand struct {
	abstractCommand
}

// Delete instantiates a DeleteCommand instance for the described Kubernetes resource.
func (f *CommandFactory) Delete(args ...string) (c Command) {
	utils.TraceCall(f.logger)
	defer utils.TraceExit(f.logger)
	c = &DeleteCommand{
		abstractCommand: abstractCommand{
			logger:     f.logger,
			factory:    f,
			subCmd:     "delete",
			jsonOutput: false,
			args:       args,
		},
	}
	return c
}

// GetCommand implements the Command interface to delete resources from the KubeAPI service
type GetCommand struct {
	abstractCommand
}

// Get instantiates a GetCommand instance for the described Kubernetes resource
func (f *CommandFactory) Get(args ...string) (c Command) {
	utils.TraceCall(f.logger)
	defer utils.TraceExit(f.logger)
	c = &GetCommand{
		abstractCommand: abstractCommand{
			logger:     f.logger,
			factory:    f,
			subCmd:     "get",
			jsonOutput: true,
			args:       args,
		},
	}
	return c
}
