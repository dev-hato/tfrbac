package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/stretchr/testify/require"
)

func Test_readTFFile(t *testing.T) {
	t.Parallel()

	filename := "resource_without_refactoring_block.tf"
	input := []byte(
		`
resource "AAA" "aaa" {
}`)
	tmpDir := t.TempDir()

	err := os.WriteFile(filepath.Join(tmpDir, filename), input, 0644)
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
	type args struct {
		input []byte
	}

	tests := map[string]struct {
		args     args
		setup    func(tmpDir string, dirName string, filename string)
		expected []byte
	}{
		"resource-with-moved-block": {
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
			setup: func(_ string, _ string, _ string) {},
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"resource-with-moved-block-symlink-cross-dir-from-tmpdir": {
			args: args{
				input: []byte(
					`
resource "AAA" "aaa" {
}

moved {
  from = "xxx"
  to = "yyy"
}`),
			},
			setup: func(tmpDir string, dirName string, filename string) {
				symLinkDir := filepath.Join(tmpDir, "sym-link-dir")

				err := os.Mkdir(symLinkDir, 0755)
				require.NoError(t, err)

				err = os.Symlink(filepath.Join("..", dirName, filename), filepath.Join(symLinkDir, filename))
				require.NoError(t, err)
			},
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"resource-without-refactoring-block": {
			args: args{
				input: []byte(
					`
resource "AAA" "aaa" {
}`),
			},
			setup: func(_ string, _ string, _ string) {},
			expected: []byte(`
resource "AAA" "aaa" {
}`),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			filename := name + ".tf"
			tmpDir := t.TempDir()
			dirName := "main-dir"
			fileDir := filepath.Join(tmpDir, dirName)

			err := os.Mkdir(fileDir, 0755)
			require.NoError(t, err)

			filePath := filepath.Join(fileDir, filename)

			err = os.WriteFile(filePath, tt.args.input, 0644)
			require.NoError(t, err)

			tt.setup(tmpDir, dirName, filename)

			origDir, err := os.Getwd()
			require.NoError(t, err)

			err = os.Chdir(tmpDir)
			require.NoError(t, err)

			t.Cleanup(func() {
				require.NoError(t, os.Chdir(origDir))
			})

			err = run()
			require.NoError(t, err)

			got, err := os.ReadFile(filePath)
			require.NoError(t, err)

			require.Equal(t, string(tt.expected), string(got))
		})
	}
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
