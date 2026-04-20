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

	if err := os.WriteFile(filepath.Join(tmpDir, filename), input, 0644); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func(root *os.Root) {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}(root)

	got, err := readTFFile(root, filename)
	require.NoError(t, err)

	require.Equal(t, input, got)
}

func Test_run(t *testing.T) {
	type args struct {
		input      []byte
		workingDir string // 実行ディレクトリ (空文字 = tmpDir)
	}

	symlinkSetup := func(tmpDir string, dirName string, filename string) {
		symLinkDir := filepath.Join(tmpDir, "sym-link-dir")

		if err := os.Mkdir(symLinkDir, 0755); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(filepath.Join("..", dirName, filename), filepath.Join(symLinkDir, filename)); err != nil {
			t.Fatal(err)
		}
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
			setup: symlinkSetup,
			expected: []byte(
				`
resource "AAA" "aaa" {
}
`),
		},
		"resource-with-moved-block-symlink-cross-dir-from-sym-link-dir": {
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
				workingDir: "sym-link-dir",
			},
			setup: symlinkSetup,
		},
		"resource-with-moved-block-symlink-cross-dir-from-main-dir": {
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
				workingDir: "main-dir",
			},
			setup: symlinkSetup,
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

			if err := os.Mkdir(fileDir, 0755); err != nil {
				t.Fatal(err)
			}

			filePath := filepath.Join(fileDir, filename)

			if err := os.WriteFile(filePath, tt.args.input, 0644); err != nil {
				t.Fatal(err)
			}

			tt.setup(tmpDir, dirName, filename)

			origDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}

			workingDir := tmpDir

			if tt.args.workingDir != "" {
				workingDir = filepath.Join(workingDir, tt.args.workingDir)
			}

			if err = os.Chdir(workingDir); err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				if chErr := os.Chdir(origDir); chErr != nil {
					t.Fatal(chErr)
				}
			})

			err = run()
			require.NoError(t, err)

			got, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatal(err)
			}

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
