package main

import (
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

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
		for _, v := range body.Blocks() {
			if slices.Contains([]string{"moved", "import"}, v.Type()) {
				body.RemoveBlock(v)
			}
		}
		if err = os.WriteFile(filePath, file.Bytes(), info.Mode()); err != nil {
			return errors.Wrap(err, "Error on os.WriteFile")
		}

		return nil
	})

	if err != nil {
		log.Fatalf("Error walking through Terraform directory: %+v\n", err)
	}
}
