//go:build unix

package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syscall.MkfifoはWindowsにないため、Unix向けのテストファイルに分けている

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
