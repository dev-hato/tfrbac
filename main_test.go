package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// runAndRequireFiles dirでrun()を実行し、エラーの有無と各ファイルの内容を確認する
// あわせて、ディレクトリ内にそれらのファイルしかないこと(一時ファイルが残っていないこと)も確認する
func runAndRequireFiles(t *testing.T, dir string, fileNames []string, wantErr bool, expected string) {
	t.Helper()

	chdirForTest(t, dir)

	if wantErr {
		require.Error(t, run())
	} else {
		require.NoError(t, run())
	}

	for _, fileName := range fileNames {
		got, err := os.ReadFile(filepath.Join(dir, fileName))
		require.NoError(t, err)
		require.Equal(t, expected, string(got), fileName)
	}

	// os.ReadDirはファイル名順に返すため、比較用にソートする
	requireDirEntryNames(t, dir, slices.Sorted(slices.Values(fileNames)))
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

	runAndRequireFiles(t, tmpDir, []string{fileName}, false, refactoringBlockExpected)

	info, err := os.Stat(filePath)
	require.NoError(t, err)
	require.Equal(t, perm, info.Mode().Perm())
}

// Test_run_handlesLongFilename ファイル名の長さが上限(255バイト)に近い.tfファイルでも、
// 一時ファイル名が長くなりすぎずにリファクタリングブロックを削除できることを確認する
func Test_run_handlesLongFilename(t *testing.T) {
	// longTFFilename 指定したバイト数の.tfファイル名を作る
	longTFFilename := func(length int) string {
		return strings.Repeat("a", length-len(".tf")) + ".tf"
	}

	tests := map[string]struct {
		fileNames []string
	}{
		"240-bytes": {
			fileNames: []string{longTFFilename(240)},
		},
		"255-bytes-max": {
			fileNames: []string{longTFFilename(255)},
		},
		// 長い名前のファイルの後に処理されるファイルも、処理が止まらずに書き換えられる
		"long-filename-followed-by-normal-file": {
			fileNames: []string{longTFFilename(240), "main.tf"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()

			for _, fileName := range tt.fileNames {
				require.NoError(t, os.WriteFile(filepath.Join(tmpDir, fileName), []byte(refactoringBlockInput), 0644))
			}

			runAndRequireFiles(t, tmpDir, tt.fileNames, false, refactoringBlockExpected)
		})
	}
}

// Test_run_doesNotChangePermissionOfSymlinkTargetWhenTempFileIsReplaced 作成した一時ファイルが
// 別ファイルへのシンボリックリンクに差し替えられても、リンク先の権限を変えないことを確認する
func Test_run_doesNotChangePermissionOfSymlinkTargetWhenTempFileIsReplaced(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "main.tf"), []byte(refactoringBlockInput), 0644))

	secretPath := filepath.Join(tmpDir, "secret.tfvars")
	secretPerm := os.FileMode(0600)
	require.NoError(t, os.WriteFile(secretPath, []byte("password = \"xxx\"\n"), secretPerm))
	require.NoError(t, os.Chmod(secretPath, secretPerm))

	// 一時ファイルの作成直後に、同じ名前のsecret.tfvarsへのシンボリックリンクに差し替える
	origHook := afterCreateTempFileHook
	afterCreateTempFileHook = func(tmpPath string) {
		require.NoError(t, os.Remove(tmpPath))
		require.NoError(t, os.Symlink("secret.tfvars", tmpPath))
	}
	t.Cleanup(func() {
		afterCreateTempFileHook = origHook
	})

	chdirForTest(t, tmpDir)
	require.NoError(t, run())

	info, err := os.Stat(secretPath)
	require.NoError(t, err)
	require.Equal(t, secretPerm, info.Mode().Perm())
}

// Test_run_doesNotWriteUnchangedFile リファクタリングブロックがないファイルは書き込まないため、
// 書き込み権限のないファイルでもエラーにならないことを確認する
func Test_run_doesNotWriteUnchangedFile(t *testing.T) {
	tmpDir := t.TempDir()
	fileName := "main.tf"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, fileName), []byte(refactoringBlockExpected), 0444))

	// 書き込みできないファイルでも、変更がなければ書き込まないためエラーにならない
	runAndRequireFiles(t, tmpDir, []string{fileName}, false, refactoringBlockExpected)
}

// Test_run_keepsOriginalFileWhenWriteFails 書き込みに失敗した場合はエラーを返し、元のファイルを壊さないことを確認する
func Test_run_keepsOriginalFileWhenWriteFails(t *testing.T) {
	tmpDir := t.TempDir()
	fileName := "main.tf"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, fileName), []byte(refactoringBlockInput), 0644))

	// ディレクトリを書き込み禁止にして、一時ファイルの作成を失敗させる
	require.NoError(t, os.Chmod(tmpDir, 0555))
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(tmpDir, 0755))
	})

	runAndRequireFiles(t, tmpDir, []string{fileName}, true, refactoringBlockInput)
}

// Test_run_keepsOriginalFileWhenHCLIsInvalid 不正なHCLの場合はエラーを返し、元のファイルを壊さず、一時ファイルも作らないことを確認する
func Test_run_keepsOriginalFileWhenHCLIsInvalid(t *testing.T) {
	const invalidInput = `
moved {
  from = "xxx"
`

	tmpDir := t.TempDir()
	fileName := "main.tf"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, fileName), []byte(invalidInput), 0644))

	runAndRequireFiles(t, tmpDir, []string{fileName}, true, invalidInput)
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
