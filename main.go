package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

func getRefactoringBlocks() []string {
	return []string{"moved", "import", "removed"}
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error on run: %+v\n", err)
	}
}

func run() (err error) {
	const terraformDir = "./" //TODO: あとで引数で弄れるようにしたい

	root, err := os.OpenRoot(terraformDir)
	if err != nil {
		return errors.Wrap(err, "Error on os.OpenRoot")
	}
	defer func(root *os.Root) {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, errors.Wrap(closeErr, "Error on Close"))
		}
	}(root)

	err = filepath.Walk(terraformDir, func(filePath string, info os.FileInfo, walkErr error) (err error) {
		if walkErr != nil {
			return errors.Wrap(walkErr, "Error on filepath.Walk")
		}

		// '.tf' 拡張子でなければスキップ
		if !strings.HasSuffix(info.Name(), ".tf") {
			return nil
		}

		mode := info.Mode()

		// シンボリックリンクやFIFOなどの通常ファイル以外はスキップ
		// (リンク先の書き換えや、FIFOの読み込みでの停止を防ぐ)
		if !mode.IsRegular() {
			return nil
		}

		relPath, err := filepath.Rel(terraformDir, filePath)
		if err != nil {
			return errors.Wrap(err, "Error on filepath.Rel")
		}

		src, err := readTFFile(root, relPath)
		if err != nil {
			return errors.Wrap(err, "Error on readTFFile")
		}

		file, diags := hclwrite.ParseConfig(src, filePath, hcl.InitialPos)
		if diags.HasErrors() {
			return errors.Wrap(diags, "Error on hclwrite.ParseConfig")
		}

		body := file.Body()
		ret := tfrbac(body).Bytes()

		// 変更がなければ書き込まない
		if bytes.Equal(src, ret) {
			return nil
		}

		if err := writeTFFile(root, relPath, ret, mode.Perm()); err != nil {
			return errors.Wrap(err, "Error on writeTFFile")
		}

		return nil
	})

	if err != nil {
		return errors.Wrap(err, "Error walking through Terraform directory")
	}

	return nil
}

func readTFFile(root *os.Root, relPath string) (src []byte, err error) {
	rf, err := root.Open(relPath)
	if err != nil {
		return nil, errors.Wrap(err, "Error on Open")
	}
	defer func(rf *os.File) {
		if closeErr := rf.Close(); closeErr != nil {
			err = errors.Join(err, errors.Wrap(closeErr, "Error on Close"))
		}
	}(rf)

	src, err = io.ReadAll(rf)
	if err != nil {
		return nil, errors.Wrap(err, "Error on io.ReadAll")
	}

	return src, nil
}

// writeTFFile 一時ファイルに書き込んでからリネームすることで、書き込み途中で失敗しても元のファイルを壊さない
func writeTFFile(root *os.Root, relPath string, data []byte, perm os.FileMode) (err error) {
	suffix := make([]byte, 8)

	if _, err = rand.Read(suffix); err != nil {
		return errors.Wrap(err, "Error on rand.Read")
	}

	// 元のファイル名を含めると、長いファイル名で上限(255バイト)を超えるため、同じディレクトリに短い名前で作る
	tmpPath := filepath.Join(filepath.Dir(relPath), ".tfrbac-"+hex.EncodeToString(suffix)+".tmp")

	// O_EXCLで、既存のファイルやシンボリックリンクを上書きしないようにする
	wf, err := root.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return errors.Wrap(err, "Error on OpenFile")
	}
	defer func() {
		if removeErr := root.Remove(tmpPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, errors.Wrap(removeErr, "Error on Remove"))
		}
	}()

	if err := writeAndClose(wf, data); err != nil {
		return errors.Wrap(err, "Error on writeAndClose")
	}

	// umaskの影響を受けないよう、元のファイルの権限を明示的に設定する
	if err := root.Chmod(tmpPath, perm); err != nil {
		return errors.Wrap(err, "Error on Chmod")
	}

	if err := root.Rename(tmpPath, relPath); err != nil {
		return errors.Wrap(err, "Error on Rename")
	}

	return nil
}

// writeAndClose データを書き込んでディスクに同期し、ファイルを閉じる
// Closeのエラーも返すため、戻り値で書き込みが最後まで完了したかを確認できる
func writeAndClose(wf *os.File, data []byte) (err error) {
	defer func() {
		if closeErr := wf.Close(); closeErr != nil {
			err = errors.Join(err, errors.Wrap(closeErr, "Error on Close"))
		}
	}()

	if _, err := wf.Write(data); err != nil {
		return errors.Wrap(err, "Error on Write")
	}

	if err := wf.Sync(); err != nil {
		return errors.Wrap(err, "Error on Sync")
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
			i++ // 後ろに改行がある場合はそれを削除
		} else if endTokenPos-2 > startTokenPos &&
			tokens[endTokenPos-1].Type == hclsyntax.TokenNewline &&
			tokens[endTokenPos-2].Type == hclsyntax.TokenNewline {
			endTokenPos-- // 後ろに改行はないけど、上に二つ以上改行がある場合、一つ削除
		}
		deleteTokens = deleteTokens[1:]

		ret = tokens[startTokenPos:endTokenPos].BuildTokens(ret)
		startTokenPos = i + 1
	}
	ret = tokens[startTokenPos:].BuildTokens(ret)
	return ret
}
