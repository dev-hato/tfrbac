package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

var version = "dev"

var defaultExcludedDirs = []string{
	".git",
	".terraform",
	".terragrunt-cache",
}

type stringSliceFlag []string

func (f *stringSliceFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringSliceFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type config struct {
	check       bool
	dryRun      bool
	excludeDirs []string
	stdout      io.Writer
	targets     []string
}

func getRefactoringBlocks() []string {
	return []string{"moved", "import", "removed"}
}

func usage() string {
	return `Usage: tfrbac [flags] [PATH...]

Remove Terraform refactoring blocks from directories or .tf files.

Flags:
  --check      Report files that would change and exit with status 1 if any are found
  --dry-run    Report files that would change without writing them
  --exclude    Directory name to skip while walking a directory. Repeatable
  --version    Print version and exit

Default excluded directories:
  .git
  .terraform
  .terragrunt-cache
`
}

func main() {
	cfg, showVersion, err := parseConfig(os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr)
		fmt.Fprint(os.Stderr, usage())
		os.Exit(2)
	}

	if showVersion {
		fmt.Fprintln(os.Stdout, version)
		return
	}

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseConfig(args []string, stdout io.Writer) (cfg config, showVersion bool, parseErr error) {
	cfg = config{
		excludeDirs: append([]string(nil), defaultExcludedDirs...),
		stdout:      stdout,
	}

	flags := flag.NewFlagSet("tfrbac", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&cfg.check, "check", false, "")
	flags.BoolVar(&cfg.dryRun, "dry-run", false, "")
	var additionalExcludes stringSliceFlag
	flags.Var(&additionalExcludes, "exclude", "")
	flags.BoolVar(&showVersion, "version", false, "")

	if err := flags.Parse(args); err != nil {
		return config{}, false, errors.Wrap(err, "parse flags")
	}

	cfg.excludeDirs = append(cfg.excludeDirs, additionalExcludes...)
	cfg.targets = flags.Args()
	if len(cfg.targets) == 0 {
		cfg.targets = []string{"."}
	}

	return cfg, showVersion, nil
}

func run(cfg config) error {
	if cfg.stdout == nil {
		cfg.stdout = io.Discard
	}
	if len(cfg.excludeDirs) == 0 {
		cfg.excludeDirs = append([]string(nil), defaultExcludedDirs...)
	}
	if len(cfg.targets) == 0 {
		cfg.targets = []string{"."}
	}

	excludedDirs := makeExcludedDirSet(cfg.excludeDirs)
	processedPaths := make(map[string]struct{})
	changedFiles := 0

	for _, target := range cfg.targets {
		count, err := processTarget(target, cfg, excludedDirs, processedPaths)
		if err != nil {
			return errors.Wrapf(err, "process target %q", target)
		}
		changedFiles += count
	}

	if cfg.check && changedFiles > 0 {
		return fmt.Errorf("refactoring blocks found in %d file(s)", changedFiles)
	}

	return nil
}

func processTarget(target string, cfg config, excludedDirs map[string]struct{}, processedPaths map[string]struct{}) (int, error) {
	target = filepath.Clean(target)

	info, err := os.Lstat(target)
	if err != nil {
		return 0, errors.Wrap(err, "stat target")
	}

	if info.IsDir() {
		return processDirectory(target, cfg, excludedDirs, processedPaths)
	}

	if filepath.Ext(info.Name()) != ".tf" {
		return 0, fmt.Errorf("target %q is not a .tf file", target)
	}

	return processSingleFile(target, info.Mode().Perm(), cfg, processedPaths)
}

func processDirectory(target string, cfg config, excludedDirs map[string]struct{}, processedPaths map[string]struct{}) (changedFiles int, resultErr error) {
	root, err := os.OpenRoot(target)
	if err != nil {
		return 0, errors.Wrap(err, "open root")
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, errors.Wrap(closeErr, "close root"))
		}
	}()

	err = filepath.WalkDir(target, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.Wrap(walkErr, "walk directory")
		}

		if entry.IsDir() {
			if filePath != target && isExcludedDir(entry.Name(), excludedDirs) {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(entry.Name(), ".tf") {
			return nil
		}

		pathKey, pathErr := uniquePathKey(filePath)
		if pathErr != nil {
			return pathErr
		}
		if _, exists := processedPaths[pathKey]; exists {
			return nil
		}
		processedPaths[pathKey] = struct{}{}

		relPath, relErr := filepath.Rel(target, filePath)
		if relErr != nil {
			return errors.Wrap(relErr, "build relative path")
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return errors.Wrap(infoErr, "read file info")
		}

		changed, processErr := processTFFile(root, relPath, filePath, info.Mode().Perm(), cfg)
		if processErr != nil {
			return processErr
		}
		if changed {
			changedFiles++
		}

		return nil
	})
	if err != nil {
		resultErr = errors.Join(resultErr, err)
		return 0, resultErr
	}

	return changedFiles, resultErr
}

func processSingleFile(target string, perm fs.FileMode, cfg config, processedPaths map[string]struct{}) (changedFiles int, err error) {
	pathKey, err := uniquePathKey(target)
	if err != nil {
		return 0, err
	}
	if _, exists := processedPaths[pathKey]; exists {
		return 0, nil
	}
	processedPaths[pathKey] = struct{}{}

	root, err := os.OpenRoot(filepath.Dir(target))
	if err != nil {
		return 0, errors.Wrap(err, "open file root")
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, errors.Wrap(closeErr, "close file root"))
		}
	}()

	changed, err := processTFFile(root, filepath.Base(target), target, perm, cfg)
	if err != nil {
		return 0, err
	}
	if !changed {
		return 0, nil
	}

	return 1, nil
}

func uniquePathKey(path string) (string, error) {
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.Wrap(err, "eval symlinks")
	}

	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return "", errors.Wrap(err, "build absolute path")
	}

	return absPath, nil
}

func makeExcludedDirSet(dirNames []string) map[string]struct{} {
	excludedDirs := make(map[string]struct{}, len(dirNames))
	for _, dirName := range dirNames {
		excludedDirs[dirName] = struct{}{}
	}
	return excludedDirs
}

func isExcludedDir(dirName string, excludedDirs map[string]struct{}) bool {
	_, exists := excludedDirs[dirName]
	return exists
}

func processTFFile(root *os.Root, relPath string, displayPath string, perm fs.FileMode, cfg config) (bool, error) {
	src, err := readTFFile(root, relPath)
	if err != nil {
		return false, errors.Wrap(err, "read terraform file")
	}

	file, diags := hclwrite.ParseConfig(src, displayPath, hcl.InitialPos)
	if diags.HasErrors() {
		return false, errors.Wrap(diags, "parse terraform file")
	}

	ret := tfrbac(file.Body()).Bytes()
	if bytes.Equal(src, ret) {
		return false, nil
	}

	if cfg.check || cfg.dryRun {
		if _, err := fmt.Fprintln(cfg.stdout, displayPath); err != nil {
			return false, errors.Wrap(err, "report changed file")
		}
		return true, nil
	}

	if err := writeTFFile(root, relPath, ret, perm); err != nil {
		return false, errors.Wrap(err, "write terraform file")
	}

	return true, nil
}

func readTFFile(root *os.Root, relPath string) (src []byte, err error) {
	rf, err := root.Open(relPath)
	if err != nil {
		return nil, errors.Wrap(err, "open file")
	}
	defer func() {
		if closeErr := rf.Close(); closeErr != nil {
			err = errors.Join(err, errors.Wrap(closeErr, "close file"))
		}
	}()

	src, err = io.ReadAll(rf)
	if err != nil {
		return nil, errors.Wrap(err, "read file")
	}

	return src, nil
}

func writeTFFile(root *os.Root, relPath string, src []byte, perm fs.FileMode) (err error) {
	wf, err := root.OpenFile(relPath, os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return errors.Wrap(err, "open file for write")
	}
	defer func() {
		if closeErr := wf.Close(); closeErr != nil {
			err = errors.Join(err, errors.Wrap(closeErr, "close file"))
		}
	}()

	if _, err := wf.Write(src); err != nil {
		return errors.Wrap(err, "write file")
	}

	return nil
}

func tfrbac(body *hclwrite.Body) hclwrite.Tokens {
	deleteTokens := make([]hclwrite.Tokens, 0)
	for _, v := range body.Blocks() {
		if slices.Contains(getRefactoringBlocks(), v.Type()) {
			deleteTokens = append(deleteTokens, v.BuildTokens(nil))
		}
	}
	tokens := body.BuildTokens(nil)
	ret := make(hclwrite.Tokens, 0, len(tokens))
	startTokenPos := 0
	for i := 0; i < len(tokens); i++ {
		if len(deleteTokens) == 0 {
			break
		}
		deleteToken := deleteTokens[0]
		find := true
		for j := 0; j < len(deleteToken) && i+j < len(tokens); j++ {
			if deleteToken[j] != tokens[i+j] {
				find = false
				break
			}
		}
		if !find {
			continue
		}

		endTokenPos := i
		i += len(deleteToken) - 1
		if i+1 < len(tokens) && tokens[i+1].Type == hclsyntax.TokenNewline {
			i++
		} else if endTokenPos-2 > startTokenPos &&
			tokens[endTokenPos-1].Type == hclsyntax.TokenNewline &&
			tokens[endTokenPos-2].Type == hclsyntax.TokenNewline {
			endTokenPos--
		}
		deleteTokens = deleteTokens[1:]

		ret = tokens[startTokenPos:endTokenPos].BuildTokens(ret)
		startTokenPos = i + 1
	}
	ret = tokens[startTokenPos:].BuildTokens(ret)
	return ret
}
