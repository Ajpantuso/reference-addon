//go:build mage
// +build mage

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	"github.com/mt-sre/go-ci/command"
	"github.com/mt-sre/go-ci/container"
	"github.com/mt-sre/go-ci/web"
)

var Aliases = map[string]interface{}{
	"build":    Build.Manager,
	"bundle":   Generate.Bundle,
	"generate": All.Generate,
	"lint":     All.Lint,
	"test":     All.Test,
}

type All mg.Namespace

// Lint ensures source code conforms to formatting standars.
func (All) Lint(ctx context.Context) {
	mg.SerialCtxDeps(
		ctx,
		All.Generate,
		Check.Tidy,
		Check.Verify,
		Check.Lint,
		Check.Dirty,
	)
}

// Test runs all unit and integration tests.
func (All) Test(ctx context.Context) {
	mg.CtxDeps(
		ctx,
		All.Generate,
		Test.Unit,
		Test.Integration,
	)
}

// Generate produces all generated pre-release artifacts.
func (All) Generate(ctx context.Context) {
	mg.CtxDeps(
		ctx,
		Generate.Manifests,
		Generate.Boilerplate,
		Generate.Deployment,
		Generate.ClusterServiceVersion,
	)
}

var _depBin = filepath.Join(_dependencyDir, "bin")

var _dependencyDir = func() string {
	if dir, ok := os.LookupEnv("DEPENDENCY_DIR"); ok {
		return dir
	}

	return filepath.Join(_projectRoot, ".cache", "dependencies")
}()

var _projectRoot = func() string {
	if root, ok := os.LookupEnv("PROJECT_ROOT"); ok {
		return root
	}

	topLevel := git(command.WithArgs{"rev-parse", "--show-toplevel"})

	if err := topLevel.Run(); err != nil || !topLevel.Success() {
		panic("failed to get working directory")
	}

	return strings.TrimSpace(topLevel.Stdout())
}()

var _module = func() string {
	module := gocmd(command.WithArgs{"mod", "why"})

	if err := module.Run(); err != nil || !module.Success() {
		panic("failed to get current branch")
	}

	lines := strings.Split(module.Stdout(), "\n")

	if len(lines) < 2 {
		panic("module not found")
	}

	return lines[1]
}()

var _version = func() string {
	const zeroVer = "v0.0.0"

	if bundleVersion, ok := os.LookupEnv("BUNDLE_VERSION"); ok {
		return bundleVersion
	}

	listTags := git(command.WithArgs{"tag", "-l"})

	if err := listTags.Run(); err != nil || !listTags.Success() {
		panic("failed to get tags")
	}

	tags := strings.Fields(listTags.Stdout())
	if len(tags) < 1 {
		return zeroVer
	}

	latest := tags[len(tags)-1]

	commitCount := git(command.WithArgs{"rev-list", latest + "..", "--count"})

	if err := commitCount.Run(); err != nil || !commitCount.Success() {
		return zeroVer
	}

	count, err := strconv.Atoi(strings.TrimSpace(commitCount.Stdout()))
	if err != nil {
		return zeroVer
	}

	if count < 1 {
		return latest
	}

	return fmt.Sprintf("%s-%d", latest, count)
}()

var _branch = func() string {
	branch := git(command.WithArgs{"rev-parse", "--abbrev-ref", "HEAD"})

	if err := branch.Run(); err != nil || !branch.Success() {
		panic("failed to get current branch")
	}

	return strings.TrimSpace(branch.Stdout())
}()

var _shortSHA = func() string {
	sha := git(command.WithArgs{"rev-parse", "--short", "HEAD"})

	if err := sha.Run(); err != nil || !sha.Success() {
		panic("failed to get short SHA")
	}

	return strings.TrimSpace(sha.Stdout())
}()

var _taggedManagerImage = _managerImageReference + ":" + _version

var _managerImageReference = func() string {
	if ref, ok := os.LookupEnv("MANAGER_IMAGE_REF"); ok {
		return ref
	}

	return "quay.io/app-sre/reference-addon-manager"
}()

var git = command.NewCommandAlias("git")

type Deps mg.Namespace

// UpdateControllerGen updates the cached controller-gen binary.
func (Deps) UpdateControllerGen(ctx context.Context) {
	mg.CtxDeps(
		ctx,
		mg.F(updateGODependency, "sigs.k8s.io/controller-tools/cmd/controller-gen"),
	)
}

// UpdateGinkgo updates the cached ginkgo binary.
func (Deps) UpdateGinkgo(ctx context.Context) {
	mg.CtxDeps(
		ctx,
		mg.F(updateGODependency, "github.com/onsi/ginkgo/v2/ginkgo"),
	)
}

// UpdateGolangCILint updates the cached golangci-lint binary.
func (Deps) UpdateGolangCILint(ctx context.Context) {
	mg.CtxDeps(
		ctx,
		mg.F(updateGODependency, "github.com/golangci/golangci-lint/cmd/golangci-lint"),
	)
}

// UpdateSetupEnvtest updates the cached setup-envtest binary.
func (Deps) UpdateSetupEnvtest(ctx context.Context) {
	mg.CtxDeps(
		ctx,
		mg.F(updateGODependency, "sigs.k8s.io/controller-runtime/tools/setup-envtest"),
	)
}

// UpdateYQ updates the cached yq binary.
func (Deps) UpdateYQ(ctx context.Context) {
	mg.CtxDeps(
		ctx,
		mg.F(updateGODependency, "github.com/mikefarah/yq/v4"),
	)
}

func updateGODependency(ctx context.Context, src string) error {
	if err := setupDepsBin(); err != nil {
		return fmt.Errorf("creating dependencies bin directory: %w", err)
	}

	toolsDir := filepath.Join(_projectRoot, "tools")

	tidy := gocmd(
		command.WithArgs{"mod", "tidy", "-compat=1.17"},
		command.WithWorkingDirectory(toolsDir),
		command.WithConsoleOut(mg.Verbose()),
		command.WithContext{Context: ctx},
	)

	if err := tidy.Run(); err != nil {
		return fmt.Errorf("starting to tidy tools dir: %w", err)
	}

	if !tidy.Success() {
		return fmt.Errorf("tidying tools dir: %w", tidy.Error())
	}

	install := gocmd(
		command.WithArgs{"install", src},
		command.WithWorkingDirectory(toolsDir),
		command.WithCurrentEnv(true),
		command.WithEnv{"GOBIN": _depBin},
		command.WithConsoleOut(mg.Verbose()),
		command.WithContext{Context: ctx},
	)

	if err := install.Run(); err != nil {
		return fmt.Errorf("starting to install command from source %q: %w", src, err)
	}

	if !install.Success() {
		return fmt.Errorf("installing command from source %q: %w", src, install.Error())
	}

	return nil
}

// UpdateOperatorSDK updates the cached operator-sdk binary.
func (Deps) UpdateOperatorSDK(ctx context.Context) error {
	const version = "v1.23.0"

	out := filepath.Join(_depBin, "operator-sdk")

	url := fmt.Sprintf(
		"https://github.com/operator-framework/operator-sdk/releases/download/%s/operator-sdk_%s_%s",
		version,
		runtime.GOOS,
		runtime.GOARCH,
	)

	if err := web.DownloadFile(ctx, url, out); err != nil {
		return fmt.Errorf("downloading operator-sdk binary: %w", err)
	}

	const perms = fs.FileMode(0755)

	if err := os.Chmod(out, perms); err != nil {
		return fmt.Errorf("changing permissions: %w", err)
	}

	return nil
}

var gocmd = command.NewCommandAlias(mg.GoCmd())

func setupDepsBin() error {
	return os.MkdirAll(_depBin, 0o774)
}

// Clean removes any existing dependency binaries
func (Deps) Clean() error {
	return sh.Rm(_depBin)
}

type Build mg.Namespace

// Manager builds the manager binary.
func (b Build) Manager(ctx context.Context) error {
	mg.CtxDeps(
		ctx,
		All.Generate,
	)

	return buildGoBinary(ctx,
		filepath.Join(_projectRoot, "cmd", "reference-addon-manager"),
		filepath.Join("bin", "linux_amd64", "reference-addon-manager"),
		withGoBuildArgs{
			"CGO_ENABLED": "0",
			"GOOS":        "linux",
			"GOARCH":      "amd64",
		},
		withLDFlags{
			"-w",
			fmt.Sprintf("-X %s/internal/version.Version=%s", _module, _version),
			fmt.Sprintf("-X %s/internal/version.Branch=%s", _module, _branch),
			fmt.Sprintf("-X %s/internal/version.Commit=%s", _module, _shortSHA),
			fmt.Sprintf("-X %s/internal/version.BuildDate=%d", _module, time.Now().Unix()),
		},
	)
}

func buildGoBinary(ctx context.Context, srcPath, outPath string, opts ...goBuildOption) error {
	cfg := newGoBuildConfig()
	cfg.Option(opts...)

	options := []command.CommandOption{
		command.WithContext{Context: ctx},
		command.WithConsoleOut(mg.Verbose()),
		command.WithCurrentEnv(true),
		command.WithEnv(cfg.Args),
		command.WithArgs{"build"},
	}

	if len(cfg.LDFlags) > 0 {
		options = append(options, command.WithArgs{
			"-ldflags", strings.Join(cfg.LDFlags, " "),
		})
	}

	options = append(options,
		command.WithArgs{"-o", outPath},
		command.WithArgs{srcPath},
	)

	build := gocmd(options...)
	if err := build.Run(); err != nil {
		return fmt.Errorf("starting to build go binary: %w", err)
	}

	if !build.Success() {
		return fmt.Errorf("building go binary: %w", build.Error())
	}

	return nil
}

// ManagerImage builds the manager container image.
func (Build) ManagerImage(ctx context.Context) {
	mg.CtxDeps(
		ctx,
		All.Generate,
		mg.F(buildImage, "Dockerfile", _taggedManagerImage, _projectRoot),
	)
}

var errNoContainerRuntime = errors.New("no container runtime")

func buildImage(ctx context.Context, file, ref, dir string) error {
	runtime, ok := container.Runtime()
	if !ok {
		return errNoContainerRuntime
	}

	build := command.NewCommand(runtime,
		command.WithContext{Context: ctx},
		command.WithConsoleOut(mg.Verbose()),
		command.WithArgs{
			"build", "-f", file, "-t", ref, dir,
		},
	)

	if err := build.Run(); err != nil {
		return fmt.Errorf("starting to build image %q: %w", ref, err)
	}

	if !build.Success() {
		return fmt.Errorf("building image %q: %w", ref, build.Error())
	}

	return nil
}

type Release mg.Namespace

// ManagerImage pushes the manager container image to the target repo.
// The target repo can be modified by setting the environment variable
// "MANAGER_IMAGE_REF" to a valid location.
func (Release) ManagerImage(ctx context.Context) {
	mg.SerialCtxDeps(
		ctx,
		Build.ManagerImage,
		mg.F(pushImage, _taggedManagerImage),
	)
}

func pushImage(ctx context.Context, ref string) error {
	runtime, ok := container.Runtime()
	if !ok {
		return errNoContainerRuntime
	}

	push := command.NewCommand(runtime,
		command.WithContext{Context: ctx},
		command.WithConsoleOut(mg.Verbose()),
		command.WithArgs{"push", ref},
	)

	if err := push.Run(); err != nil {
		return fmt.Errorf("starting to push image %q: %w", ref, err)
	}

	if !push.Success() {
		return fmt.Errorf("pushing image %q: %w", ref, push.Error())
	}

	return nil
}

type Check mg.Namespace

// Lint runs linter against source code.
func (Check) Lint(ctx context.Context) error {
	mg.CtxDeps(
		ctx,
		Deps.UpdateGolangCILint,
	)

	run := golangci(
		command.WithContext{Context: ctx},
		command.WithArgs{"run", "-v", "--fix"},
		command.WithCurrentEnv(true),
		command.WithEnv{
			"GOLANGCI_LINT_CACHE": filepath.Join(_projectRoot, ".cache", "golangci-lint"),
		},
	)

	if err := run.Run(); err != nil {
		return fmt.Errorf("starting linter: %w", err)
	}

	if run.Success() {
		return nil
	}

	fmt.Fprint(os.Stdout, run.CombinedOutput())

	return fmt.Errorf("running linter: %w", run.Error())
}

var golangci = command.NewCommandAlias(filepath.Join(_depBin, "golangci-lint"))

// Tidy ensures dependencies are correctly updated
// in the 'go.mod/ and 'go.sum' files.
func (Check) Tidy(ctx context.Context) error {
	tidy := gocmd(
		command.WithArgs{"mod", "tidy", "-compat=1.17"},
		command.WithConsoleOut(mg.Verbose()),
		command.WithContext{Context: ctx},
	)

	if err := tidy.Run(); err != nil {
		return fmt.Errorf("starting to tidy go dependencies: %w", err)
	}

	if tidy.Success() {
		return nil
	}

	return fmt.Errorf("tidying go dependencies: %w", tidy.Error())
}

// Verify ensures package dependencies have not been
// tampered with since download.
func (Check) Verify(ctx context.Context) error {
	verify := gocmd(
		command.WithArgs{"mod", "verify"},
		command.WithConsoleOut(mg.Verbose()),
		command.WithContext{Context: ctx},
	)

	if err := verify.Run(); err != nil {
		return fmt.Errorf("starting to verify go dependencies: %w", err)
	}

	if verify.Success() {
		return nil
	}

	return fmt.Errorf("verifying go dependencies: %w", verify.Error())
}

// Dirty checks if the git repository has
// uncommitted changes.
func (Check) Dirty(ctx context.Context) error {
	status := git(
		command.WithArgs{"status", "--porcelain"},
	)

	if err := status.Run(); err != nil {
		return fmt.Errorf("starting to check git status: %w", err)
	}

	if !status.Success() {
		return fmt.Errorf("checking git status: %w", status.Error())
	}

	if out := status.Stdout(); out != "" {
		return errors.New("repo is dirty")
	}

	return nil
}

type Test mg.Namespace

// Unit runs unit tests.
func (Test) Unit(ctx context.Context) error {
	mg.CtxDeps(
		ctx,
		All.Generate,
	)

	test := gocmd(
		command.WithArgs{"test", "-race", "-count=1", "-v", "./cmd/...", "./internal/..."},
		command.WithCurrentEnv(true),
		command.WithEnv{
			"CGO_ENABLED": "1",
		},
		command.WithConsoleOut(mg.Verbose()),
		command.WithContext{Context: ctx},
	)

	if err := test.Run(); err != nil {
		return fmt.Errorf("starting unit tests: %w", err)
	}

	if test.Success() {
		return nil
	}

	return fmt.Errorf("running unit tests: %w", test.Error())
}

// Integration runs integration tests.
func (Test) Integration(ctx context.Context) error {
	mg.CtxDeps(
		ctx,
		Deps.UpdateGinkgo,
		Generate.Manifests,
	)

	var assetsDir string

	if !usingExistingCluster() {
		var err error

		assetsDir, err = setupEnvTest(ctx, _depBin, "1.22.x!")
		if err != nil {
			return fmt.Errorf("setting up env-test: %w", err)
		}
	}

	test := ginkgo(
		command.WithArgs{
			"-r",
			"--randomize-all",
			"--randomize-suites",
			"--fail-on-pending",
			"--keep-going",
			"--trace",
			"--no-color",
			"-v",
			"integration",
		},
		command.WithCurrentEnv(true),
		command.WithEnv{
			"KUBEBUILDER_ASSETS": assetsDir,
			// Ensures local cache location
			"XDG_CACHE_HOME": filepath.Join(_projectRoot, ".cache"),
		},
		command.WithConsoleOut(mg.Verbose()),
		command.WithContext{Context: ctx},
	)

	if err := test.Run(); err != nil {
		return fmt.Errorf("starting integration tests: %w", err)
	}

	if test.Success() {
		return nil
	}

	return fmt.Errorf("running integration tests: %w", test.Error())
}

var ginkgo = command.NewCommandAlias(filepath.Join(_depBin, "ginkgo"))

func usingExistingCluster() bool {
	return strings.ToLower(os.Getenv("USE_EXISTING_CLUSTER")) == "true"
}

func setupEnvTest(ctx context.Context, dir, version string) (string, error) {
	mg.CtxDeps(
		ctx,
		Deps.UpdateSetupEnvtest,
	)

	setup := setupEnvtestCmd(
		command.WithArgs{
			"use", "-p", "path", "--bin-dir", _depBin, version,
		},
	)

	if err := setup.Run(); err != nil {
		return "", fmt.Errorf("starting setup: %w", err)
	}

	if !setup.Success() {
		return "", fmt.Errorf("setting up: %w", setup.Error())
	}

	return setup.Stdout(), nil
}

var setupEnvtestCmd = command.NewCommandAlias(filepath.Join(_depBin, "setup-envtest"))

type Generate mg.Namespace

// Manifests generates object manifests.
func (Generate) Manifests(ctx context.Context) error {
	mg.CtxDeps(
		ctx,
		Deps.UpdateControllerGen,
	)

	gen := controllerGen(
		command.WithArgs{
			"crd:crdVersions=v1",
			"rbac:roleName=reference-addon",
			"output:crd:artifacts:config=config/deploy",
			`paths="./apis/..."`,
			`paths="./cmd/..."`,
			`paths="./internal/..."`,
		},
		command.WithConsoleOut(mg.Verbose()),
		command.WithContext{Context: ctx},
	)

	if err := gen.Run(); err != nil {
		return fmt.Errorf("starting to generate manifests: %w", err)
	}

	if gen.Success() {
		return nil
	}

	return fmt.Errorf("generating manifests: %w", gen.Error())
}

// Boilerplate generates object boilerplate.
func (Generate) Boilerplate(ctx context.Context) error {
	mg.CtxDeps(
		ctx,
		Deps.UpdateControllerGen,
	)

	gen := controllerGen(
		command.WithArgs{
			"object", `paths="./apis/..."`,
		},
		command.WithConsoleOut(mg.Verbose()),
		command.WithContext{Context: ctx},
	)

	if err := gen.Run(); err != nil {
		return fmt.Errorf("starting to generate objects: %w", err)
	}

	if gen.Success() {
		return nil
	}

	return fmt.Errorf("generating objects: %w", gen.Error())
}

var controllerGen = command.NewCommandAlias(filepath.Join(_depBin, "controller-gen"))

// Deployment applies templated values to
// the manager Deployment.
func (Generate) Deployment(ctx context.Context) {
	var (
		template = filepath.Join("config", "templates", "deployment.tpl.yaml")
		out      = filepath.Join("config", "deploy", "deployment.yaml")
	)

	mg.CtxDeps(
		ctx,
		mg.F(yqEval, template, out,
			fmt.Sprintf(".spec.template.spec.containers[0].image = %q", _taggedManagerImage),
		),
	)
}

// ClusterServiceVersion applies templated values to
// the operator ClusterServiceVersion.
func (Generate) ClusterServiceVersion(ctx context.Context) {
	var (
		skipRange = ">0.0.0 <=" + strings.TrimPrefix(_version, "v")
		template  = filepath.Join("config", "templates", "reference-addon.csv.tpl.yaml")
		out       = filepath.Join("config", "deploy", "reference-addon.csv.yaml")
	)

	mg.CtxDeps(
		ctx,
		mg.F(yqEval, template, out,
			fmt.Sprintf(".metadata.annotations.containerImage = %q", _taggedManagerImage),
			fmt.Sprintf(`.metadata.annotations."olm.skipRange" = %q`, skipRange),
		),
	)
}

func yqEval(ctx context.Context, template, out string, exprs ...string) error {
	mg.CtxDeps(
		ctx,
		Deps.UpdateYQ,
	)

	expressions := strings.Join(exprs, " | ")

	eval := yq(
		command.WithContext{Context: ctx},
		command.WithArgs{"eval", expressions, template},
	)

	if err := eval.Run(); err != nil {
		return fmt.Errorf("starting to evaluting template %q: %w", template, err)
	}

	if !eval.Success() {
		return fmt.Errorf("evaluating template %q: %w", template, eval.Error())
	}

	const perms = fs.FileMode(0644)

	if err := os.WriteFile(out, []byte(eval.Stdout()), perms); err != nil {
		return fmt.Errorf("writing file %q: %w", out, err)
	}

	return nil
}

var yq = command.NewCommandAlias(filepath.Join(_depBin, "yq"))

// Bundle generates bundle artifacts.
func (Generate) Bundle(ctx context.Context) error {
	mg.CtxDeps(
		ctx,
		Release.ManagerImage,
		Deps.UpdateOperatorSDK,
		All.Generate,
	)

	version := strings.TrimPrefix(_version, "v")

	gen := operatorSDK(
		command.WithContext{Context: ctx},
		command.WithConsoleOut(mg.Verbose()),
		command.WithArgs{
			"generate", "bundle",
			"--package", "reference-addon",
			"--input-dir", filepath.Join("config", "deploy"),
			"--version", version,
			"--default-channel", "alpha",
			"--use-image-digests",
		},
	)

	if err := gen.Run(); err != nil {
		return fmt.Errorf("starting to generate bundles: %w", err)
	}

	if !gen.Success() {
		return fmt.Errorf("generating bundles: %w", gen.Error())
	}

	return nil
}

var operatorSDK = command.NewCommandAlias(filepath.Join(_depBin, "operator-sdk"))

// Clean removes left over bundle artifacts.
func (Release) Clean() error {
	if err := remove(filepath.Join(_projectRoot, "bundle.Dockerfile")); err != nil {
		return fmt.Errorf("removing 'bundle.Dockerfile': %w", err)
	}

	if err := removeAll(filepath.Join(_projectRoot, "bundle")); err != nil {
		return fmt.Errorf("removing bundle directory: %w", err)
	}

	return nil
}

func remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func removeAll(path string) error {
	if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func newGoBuildConfig() goBuildConfig {
	return goBuildConfig{
		Args: make(map[string]string),
	}
}

type goBuildConfig struct {
	Args    map[string]string
	LDFlags []string
}

func (c *goBuildConfig) Option(opts ...goBuildOption) {
	for _, opt := range opts {
		opt.ConfigureGoBuild(c)
	}
}

type goBuildOption interface {
	ConfigureGoBuild(*goBuildConfig)
}

type withGoBuildArgs map[string]string

func (w withGoBuildArgs) ConfigureGoBuild(c *goBuildConfig) {
	for k, v := range w {
		c.Args[k] = v
	}
}

type withLDFlags []string

func (w withLDFlags) ConfigureGoBuild(c *goBuildConfig) {
	c.LDFlags = append(c.LDFlags, []string(w)...)
}