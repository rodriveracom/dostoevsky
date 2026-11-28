package dostoevsky

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/rodriveracom/dostoevsky/internal/seam"
)

// retentionFault is a test hook called before each durable step of Retain:
// "write" (each frame written to the temp file), "sync" (temp file), "rename",
// "dirsync" and "truncate" (releasing the replaced file). A non-nil result is
// returned as if the step had failed.
var retentionFault = func(stage string) error { return seam.At("retention." + stage) }

// seedFrameBudget keeps a seed frame well under maxFrame (state is base64 in JSON).
const seedFrameBudget = 2 << 20

type retentionSeed struct {
	Partition
	Base   int64
	Groups map[string]Position
}

// Retain removes records before each supplied offset. All existing groups must
// have committed at or beyond that offset. IDs belonging to removed records
// expire. Readers below the new floor receive ErrExpired.
func (n *Node) Retain(ctx context.Context, floors map[Partition]int64) error {
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
	for p, floor := range floors {
		owner, err := n.owner(p)
		if err != nil {
			return err
		}
		if owner != n.cfg.Node {
			return ErrOwner
		}
		s := n.lookupStream(p)
		if s == nil {
			if floor != 0 {
				return ErrInvalid
			}
			continue
		}
		if floor < s.base || floor > s.base+s.count() {
			return ErrInvalid
		}
		for _, pos := range s.groups {
			if pos.Next < floor {
				return fmt.Errorf("partition %v has a lagging consumer: %w", p, ErrConflict)
			}
		}
	}
	if n.segments != nil {
		return n.segmentRetain(floors)
	}
	dir := filepath.Dir(n.j.path)
	f, err := os.CreateTemp(dir, ".retention-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	installed := false
	defer func() {
		if !installed {
			f.Close()
			os.Remove(temp)
		}
	}()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	replacement := &journal{path: n.j.path, file: f, fenced: true, synced: true}
	appendFrame := func(ev entry) (location, error) {
		frame, e := encodeFrame(ev)
		if e != nil {
			return location{}, e
		}
		loc := location{at: replacement.end + 20, size: len(frame) - 20}
		if e = retentionFault("write"); e != nil {
			return location{}, e
		}
		written, e := f.WriteAt(frame, replacement.end)
		if e != nil {
			return location{}, e
		}
		if written != len(frame) {
			return location{}, io.ErrShortWrite
		}
		replacement.end += int64(written)
		return loc, nil
	}
	if _, err = appendFrame(entry{Config: &n.cfg}); err != nil {
		return err
	}
	rebuilt := make(map[Partition]*stream, len(n.streams))
	for _, p := range partitionsInOrder(n.streams) {
		s := n.streams[p]
		if err = ctx.Err(); err != nil {
			return err
		}
		floor := s.base
		if requested, ok := floors[p]; ok {
			floor = requested
		}
		if floor > 0 || len(s.groups) > 0 {
			// Each frame is limited to maxFrame, so many large group states are split
			// over several seeds of the same base; replay merges them.
			batch := map[string]Position{}
			size := 0
			flush := func() error {
				_, e := appendFrame(entry{Seed: &retentionSeed{Partition: p, Base: floor, Groups: batch}})
				batch, size = map[string]Position{}, 0
				return e
			}
			for _, g := range groupsInOrder(s.groups) {
				pos := s.groups[g]
				cost := len(g) + len(pos.State)*4/3 + 64
				if size > 0 && size+cost > seedFrameBudget {
					if err = flush(); err != nil {
						return err
					}
				}
				batch[g] = pos
				size += cost
			}
			if err = flush(); err != nil {
				return err
			}
		}
		updated := &stream{base: floor, groups: s.groups, wait: s.wait}
		rebuilt[p] = updated
		for i := floor - s.base; i < s.count(); i++ {
			if err = ctx.Err(); err != nil {
				return err
			}
			ev, e := n.j.read(s.location(i))
			if e != nil {
				return e
			}
			loc, e := appendFrame(ev)
			if e != nil {
				return e
			}
			updated.addRecord(ev.Record, loc)
		}
	}
	if err = retentionFault("sync"); err == nil {
		err = f.Sync()
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = retentionFault("rename"); err != nil {
		return err
	}
	if err = os.Rename(temp, n.j.path); err != nil {
		return err
	}
	installed = true
	old := n.j
	replacement.lock = old.lock
	n.j = replacement
	for p, s := range n.streams {
		*s = *rebuilt[p]
		s.signal()
	}
	prev := n.retired
	n.retired = old.file
	closeErr := error(nil)
	if prev != nil {
		closeErr = prev.Close()
	}
	// The replacement is installed. If its directory entry may not be durable the
	// node cannot tell whether a crash would bring the old journal back, so it
	// stops acknowledging writes until it is reopened.
	d, e := os.Open(dir)
	if e != nil {
		n.j.failed = fmt.Errorf("journal replaced but directory not synced; reopen required: %w", e)
		return errors.Join(closeErr, n.j.failed)
	}
	syncErr := retentionFault("dirsync")
	if syncErr == nil {
		syncErr = d.Sync()
	}
	d.Close()
	if syncErr != nil && n.j.failed == nil {
		n.j.failed = fmt.Errorf("journal replaced but directory not synced; reopen required: %w", syncErr)
	}
	if n.j.failed == nil {
		// The old inode is unreachable now. Free its space but never leave it empty or
		// unfenced: a stale 0.2.x descriptor must read a header it rejects. Overwrite
		// offset 0 with one complete fence frame, cut the file to that frame and sync.
		te := retentionFault("truncate")
		if te == nil {
			var frame []byte
			if frame, te = encodeFrame(entry{Fence: 1}); te == nil {
				if _, te = old.file.WriteAt(frame, 0); te == nil {
					if te = old.file.Truncate(int64(len(frame))); te == nil {
						te = old.file.Sync()
					}
				}
			}
		}
		if te != nil {
			n.j.failed = fmt.Errorf("journal replaced but the old file could not be released; reopen required: %w", te)
		}
	}
	return errors.Join(closeErr, n.j.failed)
}
