//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/PeacherMZ/cpa-auto-refresh-quota/internal/refreshquota"
)

const (
	windowsGenericRead         = 0x80000000
	windowsGenericWrite        = 0x40000000
	windowsFileShareRead       = 0x00000001
	windowsFileShareWrite      = 0x00000002
	windowsFileShareDelete     = 0x00000004
	windowsOpenExisting        = 3
	windowsFileNormal          = 0x00000080
	windowsLockFailImmediately = 0x00000001
	windowsLockExclusive       = 0x00000002
	windowsMoveReplaceExisting = 0x00000001
	windowsMoveWriteThrough    = 0x00000008
	windowsReplaceRetryDelay   = 25 * time.Millisecond
)

var (
	kernel32DLL      = syscall.NewLazyDLL("kernel32.dll")
	replaceFileWProc = kernel32DLL.NewProc("ReplaceFileW")
	moveFileExWProc  = kernel32DLL.NewProc("MoveFileExW")
	lockFileExProc   = kernel32DLL.NewProc("LockFileEx")
	unlockFileExProc = kernel32DLL.NewProc("UnlockFileEx")
)

func replaceAuthFileCAS(ctx context.Context, targetPath, replacementPath, expectedSHA256 string) error {
	for {
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}
		errReplace := replaceAuthFileCASOnce(ctx, targetPath, replacementPath, expectedSHA256)
		if errReplace == nil || errors.Is(errReplace, refreshquota.ErrAuthFileChanged) || !isWindowsSharingError(errReplace) {
			return errReplace
		}
		timer := time.NewTimer(windowsReplaceRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func replaceAuthFileCASOnce(ctx context.Context, targetPath, replacementPath, expectedSHA256 string) error {
	targetUTF16, errTarget := syscall.UTF16PtrFromString(targetPath)
	if errTarget != nil {
		return errTarget
	}
	replacementUTF16, errReplacement := syscall.UTF16PtrFromString(replacementPath)
	if errReplacement != nil {
		return errReplacement
	}

	handle, errOpen := syscall.CreateFile(
		targetUTF16,
		windowsGenericRead|windowsGenericWrite,
		windowsFileShareRead|windowsFileShareWrite|windowsFileShareDelete,
		nil,
		windowsOpenExisting,
		windowsFileNormal,
		0,
	)
	if errOpen != nil {
		return fmt.Errorf("open target for locked replacement: %w", errOpen)
	}
	file := os.NewFile(uintptr(handle), targetPath)
	if file == nil {
		syscall.CloseHandle(handle)
		return fmt.Errorf("wrap auth file handle")
	}
	overlapped := &syscall.Overlapped{}
	lockHeld := false
	defer func() {
		if file == nil {
			return
		}
		if lockHeld {
			unlockFileExProc.Call(
				uintptr(handle),
				0,
				0xffffffff,
				0xffffffff,
				uintptr(unsafe.Pointer(overlapped)),
			)
		}
		_ = file.Close()
	}()
	locked, _, errLock := lockFileExProc.Call(
		uintptr(handle),
		windowsLockFailImmediately|windowsLockExclusive,
		0,
		0xffffffff,
		0xffffffff,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if locked == 0 {
		return fmt.Errorf("lock target for replacement: %w", errLock)
	}
	lockHeld = true
	unlocked := func() bool {
		result, _, _ := unlockFileExProc.Call(
			uintptr(handle),
			0,
			0xffffffff,
			0xffffffff,
			uintptr(unsafe.Pointer(overlapped)),
		)
		return result != 0
	}
	info, errInfo := file.Stat()
	if errInfo != nil {
		return errInfo
	}
	if !info.Mode().IsRegular() || info.Size() > maxAuthFileWriteBytes {
		return refreshquota.ErrAuthFileChanged
	}
	latest, errRead := io.ReadAll(io.LimitReader(file, maxAuthFileWriteBytes+1))
	if errRead != nil {
		return errRead
	}
	if len(latest) > maxAuthFileWriteBytes || expectedSHA256 == "" || !equalAuthFileSHA256(expectedSHA256, latest) {
		return refreshquota.ErrAuthFileChanged
	}
	if !unlocked() {
		return fmt.Errorf("unlock target before replacement")
	}
	lockHeld = false
	if errClose := file.Close(); errClose != nil {
		return errClose
	}
	file = nil
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	result, _, errCall := replaceFileWProc.Call(
		uintptr(unsafe.Pointer(targetUTF16)),
		uintptr(unsafe.Pointer(replacementUTF16)),
		0,
		0,
		0,
		0,
	)
	if result == 0 {
		if !errors.Is(errCall, syscall.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("ReplaceFileW: %w", errCall)
		}
		moved, _, errMove := moveFileExWProc.Call(
			uintptr(unsafe.Pointer(replacementUTF16)),
			uintptr(unsafe.Pointer(targetUTF16)),
			windowsMoveReplaceExisting|windowsMoveWriteThrough,
		)
		if moved == 0 {
			return fmt.Errorf("MoveFileExW fallback: %w", errMove)
		}
	}
	return nil
}

func equalAuthFileSHA256(expected string, raw []byte) bool {
	return stringsEqualFoldTrimmed(expected, authFileSHA256(raw))
}

func stringsEqualFoldTrimmed(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func isWindowsSharingError(err error) bool {
	return errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
}
