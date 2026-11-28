package dostoevsky

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/rodriveracom/dostoevsky/internal/seam"
)

// ErrIncompleteBackup is returned by Open for a directory that holds .journal.tmp
// and no journal.v1. The current Backup never produces that layout (it stages in a
// hidden sibling directory); the guard remains for directories left by an older or
// interrupted 0.3.0 candidate, so Open does not silently start an empty node there.
// Restore from a complete backup, or delete the directory to start empty.
var ErrIncompleteBackup = errors.New("incomplete backup directory")

const maxFrame = 4 << 20

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Frames written by 0.3.0 carry signatureV2. A 0.2.x binary rejects any other
// signature in recover() with "invalid journal header" and does not modify the
// file, so a directory 0.3.0 has opened can never be reopened by 0.2.x. 0.3.0
// reads both signatures.
var signature = []byte("DOST0002")
var signatureV1 = []byte("DOST0001")

type entry struct {
	Config *Config        `json:",omitempty"`
	Record *Record        `json:",omitempty"`
	Seed   *retentionSeed `json:",omitempty"`
	Check  *checkpoint    `json:",omitempty"`
	Fence  int            `json:",omitempty"` // marker frame written when 0.3.0 first opens a 0.2.x journal
}
type location struct {
	at   int64
	size int
}
type journal struct {
	path   string // canonical journal.v1; file.Name() is a temp name after Retain
	file   *os.File
	lock   *os.File
	end    int64
	failed error
	fenced bool   // the file holds at least one signatureV2 frame
	frame  []byte // scratch for single appends, protected by the node lock
	// synced is true once the file's size and metadata were made durable with a
	// full sync. Until then appends use full sync; afterwards fdatasync.
	synced bool
	torn   Truncation // the incomplete tail recover cut off, if any
}

// syncParent is syncDir, replaceable so a test can see which directories are synced.
var syncParent = syncDir

// mkdirDurable creates dir and any missing parents, then syncs every directory above dir
// on the same file system, so that a host crash cannot lose the entry of a directory that
// holds acknowledged data. All levels are synced on every call, not only new ones: an
// earlier start may have stopped, or failed a sync, part of the way, so that a directory
// exists says nothing about its durability. A failed sync is passed over only where this
// user could not have created the entry: an older directory on a read-only mount, or one
// it may neither open nor write.
func mkdirDurable(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	created := 0
	for p := abs; p != filepath.Dir(p); p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		created++
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	device := func(p string) (uint64, error) {
		info, err := os.Stat(p)
		if err != nil {
			return 0, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, fmt.Errorf("no device number for %s", p)
		}
		return uint64(st.Dev), nil
	}
	home, err := device(real)
	if err != nil {
		return err
	}
	for level, p := 0, real; p != filepath.Dir(p); level, p = level+1, filepath.Dir(p) {
		parent := filepath.Dir(p)
		on, err := device(parent)
		if err != nil {
			return err
		}
		if on != home {
			break // a mount point: nothing above it was created for this directory
		}
		err = syncParent(parent)
		if err != nil && (level < created || !cannotHaveCreated(parent, err)) {
			return err
		}
	}
	return nil
}

// oneIdentity reports whether the real and effective identities agree, replaceable so a test
// can stand for a set-user-ID process.
var oneIdentity = func() bool { return os.Getuid() == os.Geteuid() && os.Getgid() == os.Getegid() }

// cannotHaveCreated reports whether the failure to sync dir can be passed over: this user
// cannot create entries in it, so no earlier start of this program left one unsynced.
func cannotHaveCreated(dir string, err error) bool {
	if errors.Is(err, syscall.EROFS) {
		return true
	}
	// access(2) answers for the real identity, not the one that opens files.
	if !oneIdentity() {
		return false
	}
	const writable = 2 // W_OK
	probe := syscall.Access(dir, writable)
	denied := errors.Is(probe, syscall.EACCES) || errors.Is(probe, syscall.EPERM)
	return errors.Is(err, os.ErrPermission) && denied
}

// One file descriptor owns the advisory lock for the lifetime of the node.
func openJournal(dir string) (*journal, error) {
	if err := mkdirDurable(dir); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, "journal.v1")); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(dir, ".journal.tmp")); err == nil {
			return nil, fmt.Errorf("%w: %s holds .journal.tmp and no journal.v1", ErrIncompleteBackup, dir)
		}
	}
	lock, err := os.OpenFile(filepath.Join(dir, "journal.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	path := filepath.Join(dir, "journal.v1")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		lock.Close()
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		lock.Close()
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	if err != nil {
		f.Close()
		lock.Close()
		return nil, err
	}
	return &journal{path: path, file: f, lock: lock}, nil
}

func (j *journal) append(e entry) (location, error) { return j.appendWithin(e, 0) }

// appendWithin appends e unless that would take the journal past ceiling bytes (zero: no ceiling).
func (j *journal) appendWithin(e entry, ceiling int64) (location, error) {
	var loc location
	if j.failed != nil {
		return loc, j.failed
	}
	frame, err := appendEncodedFrame(j.frame[:0], e, maxFrame+20)
	if err != nil {
		return loc, err
	}
	// Avoid retaining unusually large payloads for the lifetime of the node.
	if cap(frame) <= 16<<10 {
		j.frame = frame[:0]
	}
	if ceiling > 0 && j.end+int64(len(frame)) > ceiling {
		return loc, ErrJournalFull
	}
	loc = location{j.end + 20, len(frame) - 20}
	return loc, j.writeFrames(frame)
}
func encodeFrame(e entry) ([]byte, error) {
	return appendEncodedFrame(nil, e, maxFrame+20)
}

func appendEncodedFrame(dst []byte, e entry, limit int) ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(b) > maxFrame || len(dst)+20+len(b) > limit {
		return nil, ErrLimit
	}
	dst = slices.Grow(dst, 20+len(b))
	start := len(dst)
	dst = dst[:start+20]
	h := dst[start:]
	copy(h, signature)
	binary.BigEndian.PutUint32(h[8:], uint32(len(b)))
	binary.BigEndian.PutUint32(h[12:], crc32.Checksum(b, crcTable))
	binary.BigEndian.PutUint32(h[16:], crc32.Checksum(h[:16], crcTable))
	return append(dst, b...), nil
}
func (j *journal) writeFrames(frames []byte) error {
	if j.failed != nil {
		return j.failed
	}
	err := seam.At("journal.write")
	if err == nil {
		_, err = j.file.WriteAt(frames, j.end)
	}
	if err == nil {
		err = seam.At("journal.sync")
	}
	if err == nil {
		if j.synced {
			err = syncData(j.file)
		} else if err = syncFile(j.file); err == nil {
			j.synced = true
		}
	}
	if err != nil {
		j.failed = fmt.Errorf("journal write requires reopen: %w", err)
		return j.failed
	}
	j.end += int64(len(frames))
	j.fenced = true
	return nil
}
func (j *journal) read(loc location) (entry, error) {
	var e entry
	b := make([]byte, loc.size+20)
	if _, err := j.file.ReadAt(b, loc.at-20); err != nil {
		return e, err
	}
	if !validHeader(b[:20]) || int(binary.BigEndian.Uint32(b[8:])) != loc.size || binary.BigEndian.Uint32(b[12:]) != crc32.Checksum(b[20:], crcTable) {
		return e, errors.New("journal checksum mismatch")
	}
	err := json.Unmarshal(b[20:], &e)
	return e, err
}
func validHeader(h []byte) bool {
	return (bytes.Equal(h[:8], signature) || bytes.Equal(h[:8], signatureV1)) && binary.BigEndian.Uint32(h[16:]) == crc32.Checksum(h[:16], crcTable)
}

// replayChunk (a variable so tests can shrink it) is the size of the sequential reads recovery makes. A frame is parsed in
// place from the window; the window is refilled from the frame's start when a frame
// does not fit in what is left of it, so no frame is copied and none is allocated.
var replayChunk = 1 << 20

// recover replays the journal. record, when not nil, receives Record frames that
// recordScanner fully understands (partition, offset, ID, location) without decoding
// them; every other frame, and every Record frame the scanner declines, is decoded
// with encoding/json and handed to apply, exactly as before.
func (j *journal) recover(record func(Partition, int64, string, location) error, apply func(entry, location) error) error {
	info, err := j.file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	var (
		buf   []byte
		bufAt int64
		scan  recordScanner
	)
	// window returns up to need bytes of the file starting at at: fewer only when the
	// file ends first. The slice is valid until the next call.
	window := func(at int64, need int) ([]byte, error) {
		if at >= bufAt && at+int64(need) <= bufAt+int64(len(buf)) {
			return buf[at-bufAt : at-bufAt+int64(need)], nil
		}
		want := int(min(int64(max(replayChunk, need)), size-at))
		if cap(buf) < want {
			buf = make([]byte, want)
		}
		buf = buf[:want]
		bufAt = at
		n, err := j.file.ReadAt(buf, at)
		if err != nil && !(errors.Is(err, io.EOF) && n > 0) {
			buf = buf[:0]
			return nil, err
		}
		buf = buf[:n]
		return buf[:min(need, n)], nil
	}
	for j.end < size {
		h, err := window(j.end, 20)
		if err != nil {
			return err
		}
		if len(h) < 20 {
			break
		}
		if !validHeader(h) {
			return fmt.Errorf("invalid journal header at %d", j.end)
		}
		n := int(binary.BigEndian.Uint32(h[8:]))
		if n > maxFrame || n <= 0 {
			return fmt.Errorf("invalid frame length at %d", j.end)
		}
		loc := location{j.end + 20, n}
		next := loc.at + int64(n)
		if next > size {
			// An interrupted append cannot have complete frames inside what it announced. If there are some, the length
			// is wrong, not the write unfinished: cutting here would discard them.
			later, err := laterFrames(j.file, j.end+1, size)
			if err != nil {
				return err
			}
			if later > 0 {
				return fmt.Errorf("frame at %d announces more bytes than remain, yet %d frames that verify follow: not truncating", j.end, later)
			}
			break
		}
		v2 := bytes.Equal(h[:8], signature)
		wantCRC := binary.BigEndian.Uint32(h[12:])
		frame, err := window(j.end, 20+n)
		if err != nil {
			return fmt.Errorf("frame at %d: %w", j.end, err)
		}
		if len(frame) < 20+n {
			return fmt.Errorf("frame at %d: %w", j.end, io.ErrUnexpectedEOF)
		}
		payload := frame[20:]
		if crc32.Checksum(payload, crcTable) != wantCRC {
			return fmt.Errorf("frame at %d: %w", j.end, errors.New("journal checksum mismatch"))
		}
		done := false
		if record != nil {
			if p, offset, id, ok := scan.scan(payload); ok {
				if err = record(p, offset, id, loc); err != nil {
					return fmt.Errorf("replay at %d: %w", j.end, err)
				}
				done = true
			}
		}
		if !done {
			var e entry
			if err = json.Unmarshal(payload, &e); err != nil {
				return fmt.Errorf("frame at %d: %w", j.end, err)
			}
			if err = apply(e, loc); err != nil {
				return fmt.Errorf("replay at %d: %w", j.end, err)
			}
		}
		// Only a complete frame that passed its checksum and replay counts as a fence.
		if v2 {
			j.fenced = true
		}
		j.end = next
	}
	if j.end != size {
		j.torn = Truncation{At: j.end, Bytes: size - j.end}
		if err = j.file.Truncate(j.end); err != nil {
			return err
		}
	}
	// Frames a crashed process wrote but never synced can sit in the page cache.
	// Make everything recovery accepted durable before any retry is acknowledged.
	if err = syncFile(j.file); err != nil {
		return err
	}
	j.synced = true
	return nil
}

// syncFile is a hook so tests can observe full syncs; syncData is the
// append-only data sync (fdatasync on Linux) used only once the journal exists
// durably.
var (
	syncFile = func(f *os.File) error { return f.Sync() }
	syncData = syncJournalData
)

func (j *journal) close() error { return errors.Join(j.file.Close(), j.lock.Close()) }
