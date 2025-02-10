package main

import (
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
	const terraformDir = "./" //TODO: あとで引数で弄れるようにしたい

	err := filepath.Walk(terraformDir, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrap(err, "Error on filepath.Walk")
		}

		// '.tf' 拡張子でなければスキップ
		if !strings.HasSuffix(info.Name(), ".tf") {
			return nil
		}

		src, err := os.ReadFile(filePath)
		if err != nil {
			return errors.Wrap(err, "Error on os.ReadFile")
		}

		file, diags := hclwrite.ParseConfig(src, filePath, hcl.InitialPos)
		if diags.HasErrors() {
			return errors.Wrap(diags, "Error on hclwrite.ParseConfig")
		}

		body := file.Body()
		ret := tfrbac(body)

		if err = os.WriteFile(filePath, ret.Bytes(), info.Mode()); err != nil {
			return errors.Wrap(err, "Error on os.WriteFile")
		}

		return nil
	})

	if err != nil {
		log.Fatalf("Error walking through Terraform directory: %+v\n", err)
	}
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
		if len(deleteTokens) > 0 {
			deleteToken := deleteTokens[0]
			find := true
			for j := 0; j < len(deleteToken) && i+j < len(tokens); j++ {
				if deleteToken[j] != tokens[i+j] {
					find = false
					break
				}
			}
			if find {
				endTokenPos := i
				i += len(deleteToken) - 1
				if i+1 < len(tokens) && tokens[i+1].Type == hclsyntax.TokenNewline {
					i++
				} else if endTokenPos-1 > 0 && tokens[endTokenPos-1].Type == hclsyntax.TokenNewline {
					endTokenPos--
				}
				deleteTokens = deleteTokens[1:]

				ret = tokens[startTokenPos:endTokenPos].BuildTokens(ret)
				startTokenPos = i + 1
				continue
			}
		}
	}
	ret = tokens[startTokenPos:].BuildTokens(ret)
	return ret
}
