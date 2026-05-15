package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/stretchr/testify/require"
)

func Test_parseConfig(t *testing.T) {
	t.Parallel()

	cfg, showVersion, err := parseConfig(
		[]string{"--check", "--dry-run", "--exclude", ".cache", "terraform", "main.tf"},
		io.Discard,
	)
	require.NoError(t, err)
	require.False(t, showVersion)
	require.True(t, cfg.check)
	require.True(t, cfg.dryRun)
	require.Equal(t, []string{".git", ".terraform", ".terragrunt-cache", ".cache"}, cfg.excludeDirs)
	require.Equal(t, []string{"terraform", "main.tf"}, cfg.targets)
}

func Test_parseConfig_DefaultTarget(t *testing.T) {
	t.Parallel()

	cfg, showVersion, err := parseConfig([]string{"--version"}, io.Discard)
	require.NoError(t, err)
	require.True(t, showVersion)
	require.Equal(t, []string{"."}, cfg.targets)
}

func Test_readTFFile(t *testing.T) {
	t.Parallel()

	filename := "resource_without_refactoring_block.tf"
	input := []byte(
		`
resource "AAA" "aaa" {
}`)
	tmpDir := t.TempDir()

	err := os.WriteFile(filepath.Join(tmpDir, filename), input, 0o644)
	require.NoError(t, err)

	root, err := os.OpenRoot(tmpDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, root.Close())
	})

	got, err := readTFFile(root, filename)
	require.NoError(t, err)

	require.Equal(t, input, got)
}

func Test_run(t *testing.T) {
	tests := map[string]struct {
		targetPath func(tmpDir string, filePath string) string
		setup      func(tmpDir string, dirName string, filename string)
		input      []byte
		expected   []byte
	}{
		"directory-target": {
			targetPath: func(tmpDir string, _ string) string {
				return tmpDir
			},
			setup: func(_ string, _ string, _ string) {},
			input: []byte(
				`
resource "AAA" "aaa" {
}

moved {
  from = "xxx"
  to = "yyy"
}
`),
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"single-file-target": {
			targetPath: func(_ string, filePath string) string {
				return filePath
			},
			setup: func(_ string, _ string, _ string) {},
			input: []byte(
				`
resource "AAA" "aaa" {
}

removed {
  from = aws_instance.example
}
`),
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"symlink-cross-dir-from-tmpdir": {
			targetPath: func(tmpDir string, _ string) string {
				return tmpDir
			},
			setup: func(tmpDir string, dirName string, filename string) {
				symLinkDir := filepath.Join(tmpDir, "sym-link-dir")

				err := os.Mkdir(symLinkDir, 0o755)
				require.NoError(t, err)

				err = os.Symlink(filepath.Join("..", dirName, filename), filepath.Join(symLinkDir, filename))
				require.NoError(t, err)
			},
			input: []byte(
				`
resource "AAA" "aaa" {
}

moved {
  from = "xxx"
  to = "yyy"
}`),
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			filename := name + ".tf"
			tmpDir := t.TempDir()
			dirName := "main-dir"
			fileDir := filepath.Join(tmpDir, dirName)

			err := os.Mkdir(fileDir, 0o755)
			require.NoError(t, err)

			filePath := filepath.Join(fileDir, filename)

			err = os.WriteFile(filePath, tt.input, 0o644)
			require.NoError(t, err)

			tt.setup(tmpDir, dirName, filename)

			err = run(config{
				targets: []string{tt.targetPath(tmpDir, filePath)},
				stdout:  io.Discard,
			})
			require.NoError(t, err)

			got, err := os.ReadFile(filePath)
			require.NoError(t, err)

			require.Equal(t, string(tt.expected), string(got))
		})
	}
}

func Test_run_SkipsDefaultExcludedDirs(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	includedPath := filepath.Join(tmpDir, "included.tf")
	excludedPath := filepath.Join(tmpDir, ".terraform", "excluded.tf")

	input := []byte(
		`
resource "AAA" "aaa" {
}

moved {
  from = "xxx"
  to = "yyy"
}
`)
	expected := []byte(
		`
resource "AAA" "aaa" {
}
`)

	require.NoError(t, os.MkdirAll(filepath.Dir(excludedPath), 0o755))
	require.NoError(t, os.WriteFile(includedPath, input, 0o644))
	require.NoError(t, os.WriteFile(excludedPath, input, 0o644))

	err := run(config{
		targets: []string{tmpDir},
		stdout:  io.Discard,
	})
	require.NoError(t, err)

	includedGot, err := os.ReadFile(includedPath)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(includedGot))

	excludedGot, err := os.ReadFile(excludedPath)
	require.NoError(t, err)
	require.Equal(t, string(input), string(excludedGot))
}

func Test_run_Check(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main.tf")
	input := []byte(
		`
resource "AAA" "aaa" {
}

moved {
  from = "xxx"
  to = "yyy"
}
`)
	require.NoError(t, os.WriteFile(filePath, input, 0o644))

	var stdout bytes.Buffer
	err := run(config{
		check:   true,
		targets: []string{tmpDir},
		stdout:  &stdout,
	})
	require.EqualError(t, err, "refactoring blocks found in 1 file(s)")
	assertReportedAndUnchanged(t, stdout.String(), filePath, input)
}

func Test_run_DryRun(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main.tf")
	input := []byte(
		`
resource "AAA" "aaa" {
}

import {
  to = aws_instance.example
  id = "i-abcd1234"
}
`)
	require.NoError(t, os.WriteFile(filePath, input, 0o644))

	var stdout bytes.Buffer
	err := run(config{
		dryRun:  true,
		targets: []string{tmpDir},
		stdout:  &stdout,
	})
	require.NoError(t, err)
	assertReportedAndUnchanged(t, stdout.String(), filePath, input)
}

func assertReportedAndUnchanged(t *testing.T, report string, filePath string, expectedContents []byte) {
	t.Helper()

	require.Contains(t, report, filePath)

	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, string(expectedContents), string(got))
}

func Test_tfrbac(t *testing.T) {
	t.Parallel()

	type args struct {
		input []byte
	}
	tests := map[string]struct {
		args     args
		expected []byte
	}{
		"empty": {
			args: args{
				input: []byte(""),
			},
			expected: nil,
		},
		"resource-without-refactoring-block": {
			args: args{
				input: []byte(
					`
resource "AAA" "aaa" {
}
`),
			},
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"import-block": {
			args: args{
				input: []byte(
					`
resource "aws_instance" "example" {
}

import {
  to = aws_instance.example
  id = "i-abcd1234"
}
`),
			},
			expected: []byte(
				`
resource "aws_instance" "example" {
}
`),
		},
		"removed-block": {
			args: args{
				input: []byte(
					`
removed {
  from = aws_instance.example

  lifecycle {
    destroy = false
  }
}

resource "aws_instance" "example" {
}
`),
			},
			expected: []byte(
				`
resource "aws_instance" "example" {
}
`),
		},
		"multiple-import-blocks": {
			args: args{
				input: []byte(
					`
import {
  to = aws_instance.example
  id = "i-abcd1234"
}

import {
  to = aws_s3_bucket.example
  id = "example"
}
`),
			},
			expected: []byte(
				`
`),
		},
		"mixed-refactoring-blocks-between-resources": {
			args: args{
				input: []byte(
					`
resource "aws_instance" "before" {
}

import {
  to = aws_instance.example
  id = "i-abcd1234"
}

removed {
  from = aws_instance.old
}

moved {
  from = aws_instance.old_name
  to   = aws_instance.new_name
}

resource "aws_instance" "after" {
}
`),
			},
			expected: []byte(
				`
resource "aws_instance" "before" {
}

resource "aws_instance" "after" {
}
`),
		},
		"simple-1-1": {
			args: args{
				input: []byte(
					`
resource "AAA" "aaa" {
}

moved {
  from = "xxx"
  to = "yyy"
}
`),
			},
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"simple-1-2": {
			args: args{
				input: []byte(
					`
resource "AAA" "aaa" {
}
moved {
  from = "xxx"
  to = "yyy"
}
`),
			},
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"simple-2-1": {
			args: args{
				input: []byte(
					`
moved {
  from = "xxx"
  to = "yyy"
}

resource "AAA" "aaa" {
}
`),
			},
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"simple-2-2": {
			args: args{
				input: []byte(
					`
moved {
  from = "xxx"
  to = "yyy"
}
resource "AAA" "aaa" {
}
`),
			},
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"multiple-1": {
			args: args{
				input: []byte(
					`
moved {
  from = "xxx"
  to = "yyy"
}

moved {
  from = "XXX"
  to = "YYY"
}
`),
			},
			expected: []byte(
				`
`),
		},
		"multiple-2": {
			args: args{
				input: []byte(
					`
moved {
  from = "xxx"
  to = "yyy"
}
moved {
  from = "XXX"
  to = "YYY"
}
`),
			},
			expected: []byte(
				`
`),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			file, diags := hclwrite.ParseConfig(tt.args.input, "", hcl.InitialPos)
			if diags.HasErrors() {
				require.Fail(t, diags.Error())
			}
			actual := tfrbac(file.Body())
			require.Equal(t, string(tt.expected), string(actual.Bytes()))
		})
	}
}

func Test_usage(t *testing.T) {
	t.Parallel()

	text := usage()
	require.Contains(t, text, "--check")
	require.Contains(t, text, "--dry-run")
	require.Contains(t, text, strings.Join(defaultExcludedDirs, "\n  "))
}
