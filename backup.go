package dostoevsky

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rodriveracom/dostoevsky/internal/seam"
)

// ErrBackupNotDurable reports that a backup is complete and readable but its
// directory entry may not survive a crash. The backup can be verified and used;
// repeat it or sync the parent directory before relying on it.
var ErrBackupNotDurable = errors.New("backup complete but its directory entry may not be durable")

// Backup writes a consistent journal snapshot into a new directory. The node
// remains locked during the copy. The whole backup is built in a hidden sibling
// of dir (.<name>.backup-<random>): journal.v1 is written and synced, the
// sibling is synced, dir is checked not to exist, the sibling is renamed to dir
// and the parent is synced. A crash leaves at most the hidden sibling, never a
// visible partial or empty dir; a failed backup removes its sibling. If only
// the final parent sync fails the backup is complete and the error wraps
// ErrBackupNotDurable.
//
// Caller contract: dir must be a new path that stays under the caller's control
// until Backup returns. No other process, and no other goroutine of this process
// (including a concurrent Backup to the same path), may create or use that path
// meanwhile. The existence check and the rename are two steps, and the standard
// library has no portable no-replace directory rename, so something that creates an
// empty directory at exactly that path in between would be replaced.
func (n *Node) Backup(ctx context.Context, dir string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return ErrClosed
	}
	if n.j.failed != nil {
		return n.j.failed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return publishSnapshot(dir, func(stage string) error { return n.writeBackup(ctx, stage) })
}

func publishSnapshot(dir string, write func(string) error) error {
	dir = filepath.Clean(dir)
	parent := filepath.Dir(dir)
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("backup destination exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".backup-")
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			os.RemoveAll(stage)
		}
	}()
	if err = seam.At("backup.write"); err != nil {
		return err
	}
	if err = write(stage); err != nil {
		return err
	}
	if err = syncDir(stage); err != nil {
		return err
	}
	if _, err = os.Lstat(dir); err == nil {
		return fmt.Errorf("backup destination appeared during the copy: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = seam.At("backup.rename"); err != nil {
		return err
	}
	if err = os.Rename(stage, dir); err != nil {
		return err
	}
	renamed = true
	if err = seam.At("backup.syncparent"); err == nil {
		err = syncDir(parent)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBackupNotDurable, err)
	}
	return nil
}

func (n *Node) writeBackup(ctx context.Context, stage string) error {
	f, err := os.OpenFile(filepath.Join(stage, "journal.v1"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if n.segments != nil {
		err = n.exportSegments(ctx, f)
	} else {
		buf := make([]byte, 128*1024)
		var offset int64
		for offset < n.j.end {
			if err = ctx.Err(); err != nil {
				break
			}
			size := int64(len(buf))
			if remain := n.j.end - offset; remain < size {
				size = remain
			}
			var count int
			count, err = n.j.file.ReadAt(buf[:size], offset)
			if err != nil && !errors.Is(err, io.EOF) {
				break
			}
			if int64(count) != size {
				err = io.ErrUnexpectedEOF
				break
			}
			var written int
			written, err = f.Write(buf[:count])
			if err != nil {
				break
			}
			if written != count {
				err = io.ErrShortWrite
				break
			}
			offset += int64(count)
		}
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return nil
}

// syncDir is a hook so tests can see which directories are synced.
var syncDir = func(path string) error {
	if err := seam.At("dirsync"); err != nil {
		return err
	}
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	err = d.Sync()
	return errors.Join(err, d.Close())
}

// Export a legacy journal so the backup can be restored with either engine generation.
func (n *Node) exportSegments(ctx context.Context, f *os.File) error {
	l := n.segments
	if l.failed != nil {
		return l.failed
	}
	w := bufio.NewWriterSize(f, 128<<10)
	var buf []byte
	emit := func(e entry) error {
		var err error
		buf, err = appendEncodedFrame(buf[:0], e, maxFrame+20)
		if err != nil {
			return err
		}
		_, err = w.Write(buf)
		return err
	}
	if err := emit(entry{Config: &n.cfg}); err != nil {
		return err
	}
	for _, p := range partitionsInOrder(l.state.byPart) {
		ps := l.state.parts[l.state.byPart[p]]
		seed := retentionSeed{Partition: p, Base: ps.base}
		if err := emit(entry{Seed: &seed}); err != nil {
			return err
		}
		for _, group := range groupsInOrder(ps.groups) {
			seed.Groups = map[string]Position{group: ps.groups[group]}
			if err := emit(entry{Seed: &seed}); err != nil {
				return err
			}
		}
	}
	if err := n.scanRetained(ctx, func(r record3) error {
		rec := Record{Partition: l.state.byPID[r.Partition], Offset: int64(r.Offset), Time: nanosToTime(r.Time), Message: Message{Key: string(r.Key), ID: string(r.ID), Value: r.Value}}
		return emit(entry{Record: &rec})
	}); err != nil {
		return err
	}
	return w.Flush()
}

// VerifySegments reads the physical log and checks frame checksums and retained offset continuity.
// It holds a read lock for a consistent view and can delay writers for the duration of the scan.
func (n *Node) VerifySegments(ctx context.Context) error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.closed {
		return ErrClosed
	}
	if n.segments == nil {
		return ErrInvalid
	}
	if err := checkSegmentRange(n.segments.dir); err != nil {
		return err
	}
	return n.scanRetained(ctx, nil)
}

func (n *Node) scanRetained(ctx context.Context, visit func(record3) error) error {
	l := n.segments
	next := make(map[uint32]int64, len(l.state.parts))
	for pid, ps := range l.state.parts {
		next[pid] = ps.base
	}
	for _, id := range append(append([]uint64(nil), l.ids...), l.active.id) {
		seg, _, err := scanSegmentCore(l.fs, l.dir, id, false, func(r record3, _ int64, _ int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			ps := l.state.parts[r.Partition]
			if ps == nil {
				return ErrCorruptLog
			}
			if int64(r.Offset) < ps.base {
				return nil
			}
			if int64(r.Offset) != next[r.Partition] {
				return ErrCorruptLog
			}
			next[r.Partition]++
			if visit != nil {
				return visit(r)
			}
			return nil
		}, nil)
		if err != nil {
			return err
		}
		if err = seg.f.Close(); err != nil {
			return err
		}
	}
	for pid, ps := range l.state.parts {
		if next[pid] != ps.next {
			return ErrCorruptLog
		}
	}
	return ctx.Err()
}
