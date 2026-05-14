// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

//go:build unix

package golang

import (
	"fmt"
	"os"
	"syscall"
)

// acquirePackageLock flocks the package directory itself to prevent
// concurrent edits to files in the same package. Returns an unlock
// func that callers must defer.
//
// Uses directory flock (LOCK_EX | LOCK_NB) on the package dir rather
// than a separate .lock file because the kernel automatically releases
// the lock when the process terminates (including SIGKILL and OOM
// kill), so there are no zombie lock files to clean up after a broken
// run. The non-blocking flag means a concurrent edit fails fast with a
// clear error rather than hanging.
//
// Windows uses a no-op stub (see patch_lock_windows.go) because flock
// is Unix-only; single-process concurrency on Windows is still
// protected by the higher-level sync.Mutex in the patch handler,
// cross-process protection is the missing piece there.
func acquirePackageLock(pkgDir string) (unlock func(), err error) {
	f, err := os.Open(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("open package dir for lock: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("package %s is locked by another operation — retry after it completes", pkgDir)
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
