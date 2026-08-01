//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/PeacherMZ/cpa-auto-refresh-quota/internal/refreshquota"
)

func replaceAuthFileCAS(ctx context.Context, targetPath, replacementPath, expectedSHA256 string) error {
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	latest, errRead := readAuthFileBounded(targetPath)
	if errRead != nil {
		return errRead
	}
	if strings.TrimSpace(expectedSHA256) == "" || !strings.EqualFold(expectedSHA256, authFileSHA256(latest)) {
		return refreshquota.ErrAuthFileChanged
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	if errRename := os.Rename(replacementPath, targetPath); errRename != nil {
		return errRename
	}
	if directory, errOpen := os.Open(filepath.Dir(targetPath)); errOpen == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
