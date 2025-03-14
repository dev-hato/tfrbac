package main

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/stretchr/testify/require"
)

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
