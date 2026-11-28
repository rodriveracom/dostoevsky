package seam

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"syscall"
	"time"
)

// Points are the named places where a test can make the engine fail or die. Another implementation uses the same names
// at the same boundaries. A point named for an operation is reached before that operation, unless its name says when.
var Points = []string{
	// A legacy journal append: before the write, and between the write and its sync.
	"journal.write", "journal.sync",
	// A segment append, likewise.
	"segment.write", "segment.sync",
	// Before the new range file replaces the old one.
	"range.publish",
	// Any directory sync.
	"dirsync",
	// Legacy retention: each frame of the replacement, its sync, the rename, the directory sync and the release of the
	// replaced journal.
	"retention.write", "retention.sync", "retention.rename", "retention.dirsync", "retention.truncate",
	// Backup and migration: before the copy, before the copy is published, before the parent directory is synced.
	"backup.write", "backup.rename", "backup.syncparent",
	// A workflow commit: before anything is written; part-way through writing the group (half its bytes are written,
	// at the next segment append, so arm it only where a workflow store is the thing writing); after the segment is
	// synced and before the witness is replaced; after the witness is replaced and before any caller is answered.
	"workflow.before_append", "workflow.mid_append", "workflow.after_segment_sync_before_witness",
	"workflow.after_witness_before_reply",
}

// ErrInjected is the error a point armed to fail returns.
var ErrInjected = errors.New("injected failure")

type armed struct {
	name    string
	kill    bool
	skip    atomic.Int64
	reached atomic.Bool
}

var current_ atomic.Pointer[armed]

// Arm makes the named point fail, or kill the process, the first time it is reached after skip earlier times. One point
// is armed at a time. A test must check with Disarm that the point was reached: an unreached point proves nothing.
func Arm(name string, kill bool, skip int) error {
	if !slices.Contains(Points, name) {
		return fmt.Errorf("seam: no point is named %q", name)
	}
	a := &armed{name: name, kill: kill}
	a.skip.Store(int64(skip))
	current_.Store(a)
	return nil
}

// Disarm removes the armed point and reports whether it fired.
func Disarm() bool {
	a := current_.Swap(nil)
	return a != nil && a.reached.Load()
}

// Armed reports whether the named point is the armed one, for a point that needs work done before it fires.
func Armed(name string) bool {
	a := current_.Load()
	return a != nil && a.name == name
}

// At is called by the engine at a point. It returns nil unless a test armed this point and its turn has come.
func At(name string) error {
	a := current_.Load()
	if a == nil || a.name != name || a.skip.Add(-1) >= 0 {
		return nil
	}
	a.reached.Store(true)
	if a.kill {
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL)
		// The signal is not delivered synchronously: nothing may run on after a kill point.
		for {
			time.Sleep(time.Hour)
		}
	}
	return fmt.Errorf("%w at %s", ErrInjected, name)
}
