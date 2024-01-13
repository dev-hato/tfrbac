package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

func main() {
	const terraformDir = "./" //TODO: あとで引数で弄れるようにしたい

	err := filepath.Walk(terraformDir, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// '.tf' 拡張子を持つファイルのみをリストアップ
		if strings.HasSuffix(info.Name(), ".tf") {
			src, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}

			file, diags := hclwrite.ParseConfig(src, filePath, hcl.InitialPos)
			if diags.HasErrors() {
				return err
			}

			body := file.Body()
			for _, v := range body.Blocks() {
				if slices.Contains([]string{"moved", "import"}, v.Type()) {
					body.RemoveBlock(v)
				}
			}
			if err = os.WriteFile(filePath, file.Bytes(), info.Mode()); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		fmt.Printf("Error walking through Terraform directory: %v\n", err)
	}
}
