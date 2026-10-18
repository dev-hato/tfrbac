package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

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

// chdirForTest テスト中だけカレントディレクトリを移動する
func chdirForTest(t *testing.T, dir string) {
	t.Helper()

	origDir, err := os.Getwd()
	require.NoError(t, err)

	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(origDir))
	})
}

// requireDirEntryNames ディレクトリ内のファイル名が期待通りであることを確認する
// (一時ファイルが残っていれば、そのファイル名が差分として表示される)
func requireDirEntryNames(t *testing.T, dir string, expected []string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	require.Equal(t, expected, names)
}

const refactoringBlockInput = `
resource "AAA" "aaa" {
}

moved {
  from = "xxx"
  to = "yyy"
}
`

const refactoringBlockExpected = `
resource "AAA" "aaa" {
}
`

// Test_run_doesNotModifyFileOutsideDirViaSymlink ディレクトリ外を指すシンボリックリンクがあっても、
// エラーにならず、ディレクトリ外の.tfファイルを書き換えないことを確認する
// (.tfファイルへのリンクは読み書きせず、ディレクトリへのリンクは辿らない)
func Test_run_doesNotModifyFileOutsideDirViaSymlink(t *testing.T) {
	tests := map[string]struct {
		linkName   string
		linkTarget func(outsideDir string, outsidePath string) string
	}{
		"symlink-to-file": {
			linkName:   "link.tf",
			linkTarget: func(_ string, outsidePath string) string { return outsidePath },
		},
		"symlink-to-dir": {
			linkName:   "link-dir",
			linkTarget: func(outsideDir string, _ string) string { return outsideDir },
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			outsideDir := t.TempDir()
			outsidePath := filepath.Join(outsideDir, "outside.tf")
			require.NoError(t, os.WriteFile(outsidePath, []byte(refactoringBlockInput), 0644))

			tmpDir := t.TempDir()
			require.NoError(t, os.Symlink(tt.linkTarget(outsideDir, outsidePath), filepath.Join(tmpDir, tt.linkName)))

			chdirForTest(t, tmpDir)
			require.NoError(t, run())

			got, err := os.ReadFile(outsidePath)
			require.NoError(t, err)
			require.Equal(t, refactoringBlockInput, string(got))
		})
	}
}

// Test_run_preservesPermissionAndLeavesNoTempFile リファクタリングブロックを削除した後も元のファイルの権限が保たれ、
// 一時ファイルが残らないことを確認する
func Test_run_preservesPermissionAndLeavesNoTempFile(t *testing.T) {
	tmpDir := t.TempDir()
	fileName := "main.tf"
	filePath := filepath.Join(tmpDir, fileName)
	perm := os.FileMode(0600)
	require.NoError(t, os.WriteFile(filePath, []byte(refactoringBlockInput), perm))
	require.NoError(t, os.Chmod(filePath, perm))

	chdirForTest(t, tmpDir)
	require.NoError(t, run())

	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, refactoringBlockExpected, string(got))

	info, err := os.Stat(filePath)
	require.NoError(t, err)
	require.Equal(t, perm, info.Mode().Perm())

	requireDirEntryNames(t, tmpDir, []string{fileName})
}

// Test_run_doesNotWriteUnchangedFile リファクタリングブロックがないファイルは書き込まないため、
// 書き込み権限のないファイルでもエラーにならないことを確認する
func Test_run_doesNotWriteUnchangedFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main.tf")
	require.NoError(t, os.WriteFile(filePath, []byte(refactoringBlockExpected), 0444))

	// 書き込みできないファイルでも、変更がなければ書き込まないためエラーにならない
	chdirForTest(t, tmpDir)
	require.NoError(t, run())

	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, refactoringBlockExpected, string(got))
}

// Test_run_keepsOriginalFileWhenWriteFails 書き込みに失敗した場合はエラーを返し、元のファイルを壊さないことを確認する
func Test_run_keepsOriginalFileWhenWriteFails(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main.tf")
	require.NoError(t, os.WriteFile(filePath, []byte(refactoringBlockInput), 0644))

	// ディレクトリを書き込み禁止にして、一時ファイルの作成を失敗させる
	require.NoError(t, os.Chmod(tmpDir, 0555))
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(tmpDir, 0755))
	})

	chdirForTest(t, tmpDir)
	require.Error(t, run())

	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, refactoringBlockInput, string(got))
}

// Test_run_keepsOriginalFileWhenHCLIsInvalid 不正なHCLの場合はエラーを返し、元のファイルを壊さず、一時ファイルも作らないことを確認する
func Test_run_keepsOriginalFileWhenHCLIsInvalid(t *testing.T) {
	const invalidInput = `
moved {
  from = "xxx"
`

	tmpDir := t.TempDir()
	fileName := "main.tf"
	filePath := filepath.Join(tmpDir, fileName)
	require.NoError(t, os.WriteFile(filePath, []byte(invalidInput), 0644))

	chdirForTest(t, tmpDir)
	require.Error(t, run())

	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, invalidInput, string(got))

	requireDirEntryNames(t, tmpDir, []string{fileName})
}

// Test_run_skipsFIFO .tfという名前のFIFOがあっても、読み込みで止まらずに処理を終えることを確認する
func Test_run_skipsFIFO(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(tmpDir, "fifo.tf"), 0644))

	chdirForTest(t, tmpDir)

	// FIFOを読み込もうとすると書き込み側が現れるまで止まるため、時間内に終わることを確認する
	done := make(chan error, 1)
	go func() {
		done <- run()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("run() blocked on FIFO")
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
