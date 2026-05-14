// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

//go:build windows

package golang

// acquirePackageLock is a no-op on Windows.
//
// On Unix the package-directory lock uses flock (see patch_lock_unix.go)
// to protect against concurrent edits from other techne processes
// touching the same package. The equivalent Windows API (LockFileEx)
// has different semantics and is not currently wired up; agents on
// Windows still get single-process protection via the sync.Mutex
// held in the patch handler, just not cross-process protection.
//
// This is acceptable because concurrent techne invocations against the
// same workspace are an explicit anti-pattern documented in
// docs/ARCHITECTURE.md — the lock exists to surface the mistake
// quickly, not to make it correct. Windows support of LockFileEx can
// be added later if the use case warrants it.
func acquirePackageLock(_ string) (unlock func(), err error) {
	return func() {}, nil
}
