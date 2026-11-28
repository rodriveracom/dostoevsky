package dostoevsky

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/rodriveracom/dostoevsky/internal/seam"
)

// Each append-only segment starts with a header and checkpoint. Publication uses a synced
// staging file and directory rename. Startup scans the active segment. The file-system
// interface lets tests interrupt each step and discard unsynced state.

// ErrCorruptLog reports damage detected while validating a segment or its required range.
var ErrCorruptLog = errors.New("dostoevsky: the log is corrupt: a committed segment failed validation")

// errSegUncertain: a step after the point of no return failed (the directory sync of a roll), so
// it is unknown whether the new segment is durable. The log is fenced; reopening decides.
var errSegUncertain = errors.New("dostoevsky: the log fenced itself: durability of the last step is uncertain")

type fsFile interface {
	WriteAt(p []byte, off int64) (int, error)
	ReadAt(p []byte, off int64) (int, error)
	Sync() error     // data and metadata
	SyncData() error // data (and the size, when it grew): fdatasync
	Truncate(size int64) error
	Size() (int64, error)
	Close() error
}

type fsys interface {
	OpenFile(path string, flag int, perm os.FileMode) (fsFile, error)
	Rename(oldpath, newpath string) error
	Remove(path string) error
	ReadDir(dir string) ([]string, error) // names only
	SyncDir(dir string) error
	MkdirAll(dir string, perm os.FileMode) error
}

// ----- the real file system -----

type osFS struct{}

type osFile struct{ f *os.File }

func (osFS) OpenFile(path string, flag int, perm os.FileMode) (fsFile, error) {
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return &osFile{f}, nil
}
func (osFS) Rename(a, b string) error               { return os.Rename(a, b) }
func (osFS) Remove(p string) error                  { return os.Remove(p) }
func (osFS) MkdirAll(d string, m os.FileMode) error { return os.MkdirAll(d, m) }
func (osFS) ReadDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}
func (osFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	return errors.Join(err, d.Close())
}

func (o *osFile) WriteAt(p []byte, off int64) (int, error) { return o.f.WriteAt(p, off) }
func (o *osFile) ReadAt(p []byte, off int64) (int, error)  { return o.f.ReadAt(p, off) }
func (o *osFile) Sync() error                              { return o.f.Sync() }
func (o *osFile) SyncData() error                          { return syncJournalData(o.f) }
func (o *osFile) Truncate(n int64) error                   { return o.f.Truncate(n) }
func (o *osFile) Close() error                             { return o.f.Close() }
func (o *osFile) Size() (int64, error) {
	fi, err := o.f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// ----- segment files -----

const (
	segHeaderSize = 16
	segSignature  = "DOST0003"
	segVersion    = 3
)

func segName(id uint64) string    { return fmt.Sprintf("seg-%016x.log", id) }
func segTmpName(id uint64) string { return fmt.Sprintf("seg-%016x.tmp", id) }

func parseSegName(name string) (uint64, bool) {
	hex, ok := strings.CutPrefix(name, "seg-")
	if !ok {
		return 0, false
	}
	hex, ok = strings.CutSuffix(hex, ".log")
	if !ok || len(hex) != 16 {
		return 0, false
	}
	var id uint64
	for _, c := range []byte(hex) {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		default:
			return 0, false
		}
		id = id<<4 | uint64(d)
	}
	return id, id != 0
}

func segHeader() []byte {
	h := make([]byte, 0, segHeaderSize)
	h = append(h, segSignature...)
	h = binary.LittleEndian.AppendUint16(h, segVersion)
	h = binary.LittleEndian.AppendUint16(h, 0)
	return binary.LittleEndian.AppendUint32(h, crc32.Checksum(h, crcTable))
}

func checkSegHeader(h []byte) error {
	if len(h) < segHeaderSize || string(h[:8]) != segSignature {
		return errors.New("not a segment header")
	}
	if binary.LittleEndian.Uint32(h[12:]) != crc32.Checksum(h[:12], crcTable) {
		return errors.New("segment header check failed")
	}
	if v := binary.LittleEndian.Uint16(h[8:]); v != segVersion {
		return fmt.Errorf("segment format version %d, expected %d", v, segVersion)
	}
	if f := binary.LittleEndian.Uint16(h[10:]); f != 0 {
		return fmt.Errorf("segment flags %#x", f)
	}
	return nil
}

type segment struct {
	dataStart int64
	torn      Truncation
	f         fsFile
	id        uint64
	end       int64 // the logical end: where the next append goes
	lastPos   int64 // position of the last frame, and the body check it carries (what an idx file records)
	lastCRC   uint32
}

// lastFrameOf finds the last frame in frames, which are laid out back to back starting at file
// position base, by their (already verified) lengths.
func lastFrameOf(frames []byte, base int64) (pos int64, crc uint32, err error) {
	at := 0
	for at < len(frames) {
		if len(frames)-at < frame3Header {
			return 0, 0, errFrame3Short
		}
		n := int(binary.LittleEndian.Uint32(frames[at:]))
		if n > frame3Max || at+frame3Header+n > len(frames) {
			return 0, 0, errFrame3Short
		}
		pos, crc = base+int64(at), binary.LittleEndian.Uint32(frames[at+10:])
		at += frame3Header + n
	}
	if at == 0 {
		return 0, 0, errFrame3Short
	}
	return pos, crc, nil
}

// createSegment publishes a new segment whose content is the header and the checkpoint cp
// (stamped with the segment's id). The order is the contract: stage as seg-<id>.tmp, write, fsync
// the file, rename to seg-<id>.log, fsync the directory; only then may anything be appended. A
// failure before the rename removes the staging file and leaves the log as it was; a failure of
// the directory sync returns errSegUncertain (the segment may or may not survive a crash, and
// nothing has been acknowledged in it either way).
func createSegment(fs fsys, dir string, id uint64, cp checkpoint3) (*segment, error) {
	cp.Segment = id
	data, err := appendCheckpoint3(segHeader(), cp)
	if err != nil {
		return nil, err
	}
	tmp, final := filepath.Join(dir, segTmpName(id)), filepath.Join(dir, segName(id))
	f, err := fs.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	abandon := func(err error) (*segment, error) {
		f.Close()
		fs.Remove(tmp) // best effort: Open removes leftover staging files anyway
		return nil, err
	}
	if n, err := f.WriteAt(data, 0); err != nil || n != len(data) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return abandon(err)
	}
	if err = f.Sync(); err != nil {
		return abandon(err)
	}
	if err = fs.Rename(tmp, final); err != nil {
		return abandon(err)
	}
	if err = fs.SyncDir(dir); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %v", errSegUncertain, err)
	}
	lastPos, lastCRC, err := lastFrameOf(data[segHeaderSize:], segHeaderSize)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &segment{dataStart: int64(len(data)), f: f, id: id, end: int64(len(data)), lastPos: lastPos, lastCRC: lastCRC}, nil
}

// append writes frames at the end and syncs. On any error the caller fences the log: a partial
// write may exist past s.end and the durability of the sync is unknown.
func (s *segment) append(frames []byte) error {
	lastPos, lastCRC, err := lastFrameOf(frames, s.end)
	if err != nil {
		return fmt.Errorf("segment write: %w", err)
	}
	if seam.Armed("workflow.mid_append") {
		// Leave the group half written, as an interrupted write would.
		if _, err = s.f.WriteAt(frames[:len(frames)/2], s.end); err == nil {
			err = seam.At("workflow.mid_append")
		}
		if err != nil {
			return fmt.Errorf("segment write: %w", err)
		}
	}
	if err = seam.At("segment.write"); err != nil {
		return fmt.Errorf("segment write: %w", err)
	}
	n, err := s.f.WriteAt(frames, s.end)
	if err == nil && n != len(frames) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = seam.At("segment.sync")
	}
	if err == nil {
		err = s.f.SyncData()
	}
	if err != nil {
		return fmt.Errorf("segment write: %w", err)
	}
	s.end += int64(len(frames))
	s.lastPos, s.lastCRC = lastPos, lastCRC
	return nil
}

// ----- reading frames -----

// frameReader reads frames sequentially from a file through a window, with no allocation per
// frame. A frame that straddles the end of the window makes it refill from that frame's start,
// large enough for any valid frame, so "short" is only ever reported when the end of the file is
// really inside the frame.
type frameReader struct {
	f     fsFile
	size  int64
	buf   []byte
	bufAt int64
	chunk int
}

func (r *frameReader) fill(off int64, want int) error {
	want = int(min(int64(want), r.size-off))
	if cap(r.buf) < want {
		r.buf = make([]byte, want)
	}
	r.buf = r.buf[:want]
	r.bufAt = off
	n, err := r.f.ReadAt(r.buf, off)
	if err != nil && !(errors.Is(err, io.EOF) && n == want) {
		r.buf = r.buf[:0]
		return err
	}
	return nil
}

func (r *frameReader) next(off int64) (typ byte, body []byte, n int, err error) {
	if off < r.bufAt || off >= r.bufAt+int64(len(r.buf)) {
		chunk := r.chunk
		if chunk == 0 {
			chunk = replayChunk
		}
		if err = r.fill(off, chunk); err != nil {
			return 0, nil, 0, err
		}
	}
	typ, body, n, err = readFrame3(r.buf[off-r.bufAt:])
	if errors.Is(err, errFrame3Short) && r.bufAt+int64(len(r.buf)) < r.size {
		// Refill the header first if it straddled the window boundary.
		want := frame3Header
		if b := r.buf[off-r.bufAt:]; len(b) >= frame3Header {
			want += int(binary.LittleEndian.Uint32(b))
		}
		if want == frame3Header && len(r.buf[off-r.bufAt:]) < frame3Header {
			if err = r.fill(off, frame3Header); err != nil {
				return
			}
			_, _, _, headerErr := readFrame3(r.buf)
			if !errors.Is(headerErr, errFrame3Short) {
				return readFrame3(r.buf)
			}
			if len(r.buf) < frame3Header {
				return 0, nil, 0, errFrame3Short
			}
			want += int(binary.LittleEndian.Uint32(r.buf))
		}
		chunk := r.chunk
		if chunk == 0 {
			chunk = replayChunk
		}
		if err = r.fill(off, max(chunk, want)); err != nil {
			return 0, nil, 0, err
		}
		typ, body, n, err = readFrame3(r.buf[off-r.bufAt:])
	}
	return
}

// CorruptLogError says where a committed segment failed validation. It matches ErrCorruptLog with
// errors.Is and carries the segment, the byte offset (-1 when the whole segment is meant, as for a
// missing one) and the reason, for errors.As.
type CorruptLogError struct {
	Segment uint64
	Offset  int64
	Reason  string
}

func (e *CorruptLogError) Error() string {
	if e.Offset < 0 {
		return fmt.Sprintf("%v: segment %d: %s", ErrCorruptLog, e.Segment, e.Reason)
	}
	return fmt.Sprintf("%v: segment %d at offset %d: %s", ErrCorruptLog, e.Segment, e.Offset, e.Reason)
}

func (e *CorruptLogError) Unwrap() error { return ErrCorruptLog }

func corrupt(id uint64, at int64, format string, a ...any) error {
	return &CorruptLogError{Segment: id, Offset: at, Reason: fmt.Sprintf(format, a...)}
}

// scanSegment is scanSegmentCore for the active segment: it may truncate a torn tail.
func scanSegment(fs fsys, dir string, id uint64, onRecord func(rec record3, at int64, size int) error) (*segment, *logState, error) {
	return scanSegmentCore(fs, dir, id, true, onRecord, nil)
}

// scanSegmentCore opens segment id, validates its header and start checkpoint, replays every frame
// after it into the state the checkpoint described, and returns the segment ready for appending
// together with that state.
//
// The torn-tail rule is the one the journal has always had, made precise by the frame header
// check: an incomplete final frame (the end of the file is inside a frame whose header
// verifies) is a torn append and the file is truncated at that frame's start; a retention
// checkpoint that is incomplete at the end is truncated at its first frame; anything else that
// does not validate (a header or checksum failure, a frame that does not follow the state, an
// unreadable start checkpoint) is ErrCorruptLog, and nothing is changed. A sealed segment is
// complete, so with mayTruncate false any torn tail is ErrCorruptLog too and the file is never
// written. onStart, when set, is called once, right after the start checkpoint is adopted, with
// the state it describes; onRecord, when set, is called for every record after it with the
// frame's position and size, in order. Their slices alias a buffer that is reused.
func scanSegmentCore(fs fsys, dir string, id uint64, mayTruncate bool, onRecord func(rec record3, at int64, size int) error, onStart func(st *logState)) (*segment, *logState, error) {
	flag := os.O_RDWR
	if !mayTruncate {
		flag = os.O_RDONLY
	}
	f, err := fs.OpenFile(filepath.Join(dir, segName(id)), flag, 0)
	if err != nil {
		return nil, nil, err
	}
	seg, st, err := func() (*segment, *logState, error) {
		size, err := f.Size()
		if err != nil {
			return nil, nil, err
		}
		if size < segHeaderSize {
			return nil, nil, corrupt(id, 0, "%d bytes, shorter than the header", size)
		}
		hdr := make([]byte, segHeaderSize)
		if _, err = f.ReadAt(hdr, 0); err != nil {
			return nil, nil, err
		}
		if err = checkSegHeader(hdr); err != nil {
			return nil, nil, corrupt(id, 0, "%v", err)
		}
		st := newLogState()
		r := newReplayer3(st)
		r.WantSegment = &id
		var curAt int64
		var curSize int
		var callbackErr error
		if onRecord != nil {
			r.OnRecord = func(rec record3) error { callbackErr = onRecord(rec, curAt, curSize); return callbackErr }
		}
		fr := &frameReader{f: f, size: size}
		at, cpStart, torn := int64(segHeaderSize), int64(-1), false
		var lastPos, beforeCpPos int64
		var lastCRC, beforeCpCRC uint32
		var dataStart int64
		started := false
		for at < size {
			typ, body, n, err := fr.next(at)
			if err != nil {
				if errors.Is(err, errFrame3Short) {
					torn = true
					break
				}
				return nil, nil, corrupt(id, at, "%v", err)
			}
			if at == segHeaderSize && typ != f3CpBegin {
				return nil, nil, corrupt(id, at, "the first frame is type %d, not the start checkpoint", typ)
			}
			if typ == f3CpBegin && !r.InCheckpoint() {
				cpStart, beforeCpPos, beforeCpCRC = at, lastPos, lastCRC
			}
			curAt, curSize = at, n
			if err = r.Frame(typ, body); err != nil {
				if callbackErr != nil {
					return nil, nil, callbackErr
				}
				return nil, nil, corrupt(id, at, "%v", err)
			}
			lastPos, lastCRC = at, binary.LittleEndian.Uint32(fr.buf[at-fr.bufAt+10:])
			at += int64(n)
			if !started && r.Checkpoints == 1 {
				started = true
				dataStart = at
				if onStart != nil {
					onStart(st)
				}
			}
		}
		if r.InCheckpoint() {
			if r.Checkpoints == 0 {
				return nil, nil, corrupt(id, cpStart, "the start checkpoint is incomplete")
			}
			at, torn, lastPos, lastCRC = cpStart, true, beforeCpPos, beforeCpCRC // an incomplete retention checkpoint
		}
		if r.Checkpoints == 0 {
			return nil, nil, corrupt(id, segHeaderSize, "no start checkpoint")
		}
		if torn || at < size {
			if !mayTruncate {
				return nil, nil, corrupt(id, at, "a sealed segment has a torn or partial tail")
			}
			if err = f.Truncate(at); err != nil {
				return nil, nil, err
			}
		}
		// Even a complete active frame may have survived a process crash only in page cache.
		if mayTruncate {
			if err = f.Sync(); err != nil {
				return nil, nil, err
			}
		}
		return &segment{dataStart: dataStart, torn: Truncation{At: at, Bytes: size - at}, f: f, id: id, end: at, lastPos: lastPos, lastCRC: lastCRC}, st, nil
	}()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return seg, st, nil
}

// ----- the directory of segments -----

// segPins counts the readers (and backups) holding a sealed segment, so retention can retire a
// segment while it is in use and have it unlinked by whoever lets go last. Callers hold segLog.mu.
type segPins struct {
	n    map[uint64]int
	dead map[uint64]bool
}

func (p *segPins) acquire(id uint64) bool {
	if p.dead[id] {
		return false
	}
	if p.n == nil {
		p.n = map[uint64]int{}
	}
	p.n[id]++
	return true
}

// release lets go; it reports whether the segment was retired and this was the last holder.
func (p *segPins) release(id uint64) bool {
	if p.n[id]--; p.n[id] <= 0 {
		delete(p.n, id)
		if p.dead[id] {
			delete(p.dead, id)
			return true
		}
	}
	return false
}

// retire marks a segment dead; it reports whether nobody holds it (so it can go at once).
func (p *segPins) retire(id uint64) bool {
	if p.n[id] > 0 {
		if p.dead == nil {
			p.dead = map[uint64]bool{}
		}
		p.dead[id] = true
		return false
	}
	return true
}

var (
	errSegNotFound = errors.New("segment log: no such record")
	errSegGone     = errors.New("segment log: the segment was retired")
)

const idxCacheSize = 32

type segLog struct {
	publishBuf []byte         // bounded frame encoding scratch, protected by the node writer lock
	filters    *idFilterCache // only used by the public node under its write lock
	idProbes   uint64         // exact sealed ID lookups under the node write lock
	indexLoads uint64         // cold index loads under indexLoad lock
	bytes      int64
	fs         fsys
	dir        string
	indexLoad  sync.Mutex // serializes cold index loads and publication of rebuilt sidecars
	mu         sync.Mutex // guards ids, cache and pins
	ids        []uint64   // sealed segment ids, ascending (the active segment is not in it)
	active     *segment
	state      *logState // as of Open: the state the active segment replays to
	idx        *segIndex // the active segment's index: built by the scan, extended by observe
	failed     error     // set by a failed write or roll: the log refuses further writes until reopened

	cache        map[uint64]*segIndex // sealed segments' indexes, verified; a small bounded cache
	frequency    *indexFrequency
	cacheIDBytes int
	cacheOrder   []uint64
	pins         segPins

	sealAsync bool // write a sealed segment's idx file in the background (the engine sets it); tests run it inline
	wg        sync.WaitGroup
	rebuilds  int   // indexes rebuilt by scanning a segment because the idx file was missing or wrong
	idxErr    error // the last failure to write an idx file (a hint: never fatal)
}

// openSegLog lists the directory, removes staging files, checks that the segment ids are
// contiguous, and scans the highest segment, building its index. A directory with no segment opens
// as an empty log the caller starts with start. Anything wrong with a committed segment is
// ErrCorruptLog.
func openSegLog(fs fsys, dir string) (*segLog, error) {
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	names, err := fs.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for _, name := range names {
		if strings.HasPrefix(name, "seg-") && strings.HasSuffix(name, ".tmp") {
			// a staged segment or idx that was never published: nothing acknowledged is in it
			if err = fs.Remove(filepath.Join(dir, name)); err != nil {
				return nil, err
			}
			continue
		}
		if id, ok := parseSegName(name); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			return nil, &CorruptLogError{Segment: ids[i-1] + 1, Offset: -1, Reason: fmt.Sprintf("the segment is missing (segments %d and %d are present)", ids[i-1], ids[i])}
		}
	}
	l := &segLog{fs: fs, dir: dir, cache: map[uint64]*segIndex{}}
	if len(ids) == 0 {
		l.state = newLogState()
		return l, nil
	}
	top := ids[len(ids)-1]
	idx := newSegIndex(nil)
	l.active, l.state, err = scanSegmentCore(fs, dir, top, true,
		func(rec record3, at int64, _ int) error { idx.observe(rec, at); return nil },
		func(st *logState) { idx.startNext = startNextOf(st) })
	if err != nil {
		return nil, err
	}
	l.idx = idx
	l.ids = ids[:len(ids)-1]
	return l, nil
}

// start publishes the first segment of an empty log.
func (l *segLog) start(cp checkpoint3) error {
	if l.active != nil || len(l.ids) != 0 {
		return errors.New("segment log already started")
	}
	st := newLogState()
	if err := st.load(cp); err != nil {
		return err
	}
	seg, err := createSegment(l.fs, l.dir, 1, cp)
	if err != nil {
		l.failed = err
		return err
	}
	l.active, l.state, l.idx = seg, st, newSegIndex(startNextOf(st))
	return nil
}

// observe records, in the active segment's index, a record frame that append has just made durable
// at pos. The engine calls it after a successful append, as it updates the rest of its state.
func (l *segLog) observe(rec record3, pos int64) { l.idx.observe(rec, pos) }

func (l *segLog) append(frames []byte) error {
	if l.failed != nil {
		return l.failed
	}
	if l.active == nil {
		return errors.New("segment log not started")
	}
	if err := l.active.append(frames); err != nil {
		l.failed = err
		return err
	}
	return nil
}

// roll publishes the next segment, whose start checkpoint is cp, and makes it the active one. Any
// failure fences the log (the staging file is removed if the rename did not happen). The segment
// that was active is sealed: its index is kept in the cache and written beside it as an idx file.
func (l *segLog) roll(cp checkpoint3) error {
	if l.failed != nil {
		return l.failed
	}
	if l.active == nil {
		return errors.New("segment log not started")
	}
	st := newLogState()
	if err := st.load(cp); err != nil {
		return err
	}
	seg, err := createSegment(l.fs, l.dir, l.active.id+1, cp)
	if err != nil {
		l.failed = err
		return err
	}
	old, oldIdx := l.active, l.idx
	info := idxFooterInfo{segment: old.id, size: uint64(old.end), lastPos: uint64(old.lastPos), lastCRC: old.lastCRC}
	l.mu.Lock()
	l.ids = append(l.ids, old.id)
	oldIdx.footer = info
	l.cachePut(old.id, oldIdx)
	l.mu.Unlock()
	if l.filters != nil {
		if oldIdx.ids.n == 0 {
			l.filters.put(old.id, idFilter{})
		} else {
			l.rememberFilter(old.id, oldIdx)
		}
	}
	old.f.Close()
	l.active, l.idx = seg, newSegIndex(startNextOf(st))
	write := func() {
		if err := l.writeIdxFile(old.id, encodeSegIndex(oldIdx, info)); err != nil {
			l.mu.Lock()
			l.idxErr = err
			l.mu.Unlock()
		}
	}
	if l.sealAsync {
		l.wg.Add(1)
		go func() { defer l.wg.Done(); write() }()
	} else {
		write()
	}
	return nil
}

func (l *segLog) close() error {
	l.wg.Wait()
	if l.active == nil {
		return nil
	}
	return l.active.f.Close()
}

// ----- idx files and the index cache -----

// writeIdxFile publishes seg-<id>.idx the way a segment is published: stage, write, fsync, rename,
// fsync the directory. A failure leaves no idx file, which only costs a rebuild later.
func (l *segLog) writeIdxFile(id uint64, data []byte) error {
	return l.writeHint(idxName(id), data)
}
func (l *segLog) writeHint(name string, data []byte) error {
	tmp, final := filepath.Join(l.dir, name+".tmp"), filepath.Join(l.dir, name)
	l.fs.Remove(tmp) // a leftover from a crashed attempt
	f, err := l.fs.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	abandon := func(err error) error {
		f.Close()
		l.fs.Remove(tmp)
		return err
	}
	if n, err := f.WriteAt(data, 0); err != nil || n != len(data) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return abandon(err)
	}
	if err = f.Sync(); err != nil {
		return abandon(err)
	}
	if err = l.fs.Rename(tmp, final); err != nil {
		return abandon(err)
	}
	f.Close()
	return l.fs.SyncDir(l.dir)
}

// cachePut stores a verified index, evicting the oldest when the cache is full. Callers hold l.mu.
func (l *segLog) cachePut(id uint64, x *segIndex) {
	if l.frequency != nil {
		l.admitIndex(id, x)
		return
	}
	if _, ok := l.cache[id]; !ok {
		l.cacheOrder = append(l.cacheOrder, id)
	}
	if prev := l.cache[id]; prev != nil {
		l.cacheIDBytes -= len(prev.ids.keys) * 16
	}
	l.cache[id] = x
	l.cacheIDBytes += len(x.ids.keys) * 16
	for len(l.cacheOrder) > idxCacheSize || (len(l.cacheOrder) > 1 && l.cacheIDBytes > 8<<20) {
		l.cacheIDBytes -= len(l.cache[l.cacheOrder[0]].ids.keys) * 16
		delete(l.cache, l.cacheOrder[0])
		l.cacheOrder = l.cacheOrder[1:]
	}
}

// loadIdxFile reads seg-<id>.idx, validates it as a whole and checks it against the segment it
// claims to describe: the segment id, the segment's size, and the last frame (found at the recorded
// position, whole and valid, ending exactly at the segment's end, carrying the recorded check).
func (l *segLog) loadIdxFile(id uint64) (*segIndex, error) {
	f, err := l.fs.OpenFile(filepath.Join(l.dir, idxName(id)), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size, err := f.Size()
	if err != nil {
		return nil, err
	}
	// An oversized derived file can always be reconstructed from the verified segment.
	if size > 32<<20 {
		return nil, fmt.Errorf("%w: %d bytes", errIdxInvalid, size)
	}
	b := make([]byte, size)
	if _, err = f.ReadAt(b, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	x, info, err := decodeSegIndex(b)
	if err != nil {
		return nil, err
	}
	if err = l.checkIndexSegment(id, info); err != nil {
		return nil, err
	}
	x.footer = info
	return x, nil
}
func (l *segLog) checkIndexSegment(id uint64, info idxFooterInfo) error {
	seg, err := l.fs.OpenFile(filepath.Join(l.dir, segName(id)), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer seg.Close()
	segSize, err := seg.Size()
	if err != nil {
		return err
	}
	if info.segment != id || info.size != uint64(segSize) || info.lastPos < segHeaderSize || info.lastPos >= info.size {
		return fmt.Errorf("%w: it describes segment %d of %d bytes, not segment %d of %d", errIdxInvalid, info.segment, info.size, id, segSize)
	}
	fr := &frameReader{f: seg, size: segSize}
	_, _, n, err := fr.next(int64(info.lastPos))
	if err != nil || int64(info.lastPos)+int64(n) != segSize || binary.LittleEndian.Uint32(fr.buf[int64(info.lastPos)-fr.bufAt+10:]) != info.lastCRC {
		return fmt.Errorf("%w: the last frame does not match the segment", errIdxInvalid)
	}
	return nil
}

// rebuildIdx scans one sealed segment (read-only) and builds its index, then tries to write it back.
func (l *segLog) rebuildIdx(id uint64) (*segIndex, error) {
	idx := newSegIndex(nil)
	seg, _, err := scanSegmentCore(l.fs, l.dir, id, false,
		func(rec record3, at int64, _ int) error { idx.observe(rec, at); return nil },
		func(st *logState) { idx.startNext = startNextOf(st) })
	if err != nil {
		return nil, err
	}
	seg.f.Close()
	l.mu.Lock()
	l.rebuilds++
	l.mu.Unlock()
	info := idxFooterInfo{segment: id, size: uint64(seg.end), lastPos: uint64(seg.lastPos), lastCRC: seg.lastCRC}
	idx.footer = info
	if err := l.writeIdxFile(id, encodeSegIndex(idx, info)); err != nil {
		l.mu.Lock()
		l.idxErr = err
		l.mu.Unlock()
	}
	return idx, nil
}

// sealedIndex returns a sealed segment's verified index: from the cache, else from its idx file,
// else rebuilt by scanning the segment. The idx file is a hint; the segment decides.
func (l *segLog) sealedIndex(id uint64) (*segIndex, error) {
	l.mu.Lock()
	if l.frequency != nil {
		l.frequency.observe(id)
	}
	if x, ok := l.cache[id]; ok {
		l.mu.Unlock()
		return x, nil
	}
	l.mu.Unlock()
	l.indexLoad.Lock()
	defer l.indexLoad.Unlock()
	l.mu.Lock()
	x := l.cache[id]
	l.mu.Unlock()
	if x != nil {
		return x, nil
	}
	l.indexLoads++
	x, err := l.loadIdxFile(id)
	if err != nil {
		if x, err = l.rebuildIdx(id); err != nil {
			return nil, err
		}
	}
	l.mu.Lock()
	l.cachePut(id, x)
	l.mu.Unlock()
	return x, nil
}

// ----- finding and reading records -----

// locate finds the segment that holds record off of partition pid and a position at or before it.
// The active segment is answered from memory; for older data the sealed segments are binary-searched
// by the partition's first offset in each (which never decreases from one segment to the next).
func (l *segLog) locate(pid uint32, off uint64) (id uint64, pos int64, err error) {
	if l.idx != nil && off >= l.idx.first(pid) {
		if p, ok := l.idx.find(pid, off); ok {
			return l.active.id, p, nil
		}
		return 0, 0, errSegNotFound
	}
	l.mu.Lock()
	ids := slices.Clone(l.ids)
	l.mu.Unlock()
	var searchErr error
	i := sort.Search(len(ids), func(i int) bool {
		x, e := l.sealedIndex(ids[i])
		if e != nil {
			searchErr = e
			return true
		}
		return x.first(pid) > off
	}) - 1
	if searchErr != nil {
		return 0, 0, searchErr
	}
	if i < 0 {
		return 0, 0, errSegNotFound
	}
	x, err := l.sealedIndex(ids[i])
	if err != nil {
		return 0, 0, err
	}
	p, ok := x.find(pid, off)
	if !ok {
		return 0, 0, errSegNotFound
	}
	return ids[i], p, nil
}

func (l *segLog) pin(id uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pins.acquire(id)
}

func (l *segLog) unpin(id uint64) {
	l.mu.Lock()
	gone := l.pins.release(id)
	l.mu.Unlock()
	if gone {
		l.removeSegmentFiles(id)
	}
}

func (l *segLog) removeSegmentFiles(id uint64) {
	l.fs.Remove(filepath.Join(l.dir, segName(id)))
	l.fs.Remove(filepath.Join(l.dir, idxName(id)))
	l.fs.Remove(filepath.Join(l.dir, filterName(id)))
}

// retire takes a sealed segment out of the log (retention): it is no longer found, and its files
// are removed now, or by the last reader or backup that holds it.
func (l *segLog) retire(id uint64) {
	if l.filters != nil {
		l.filters.remove(id)
	}
	l.mu.Lock()
	if i := slices.Index(l.ids, id); i >= 0 {
		l.ids = slices.Delete(l.ids, i, i+1)
	}
	if x := l.cache[id]; x != nil {
		l.cacheIDBytes -= len(x.ids.keys) * 16
	}
	delete(l.cache, id)
	l.cacheOrder = slices.DeleteFunc(l.cacheOrder, func(x uint64) bool { return x == id })
	now := l.pins.retire(id)
	l.mu.Unlock()
	if now {
		l.removeSegmentFiles(id)
	}
}

// readRecord returns record off of partition pid, copied out of the file buffer.
func (l *segLog) readRecord(pid uint32, off uint64) (record3, error) {
	id, pos, err := l.locate(pid, off)
	if err != nil {
		return record3{}, err
	}
	return l.readLocatedRecord(id, pos, pid, off)
}

func (l *segLog) readLocatedRecord(id uint64, pos int64, pid uint32, off uint64) (record3, error) {
	var err error
	var f fsFile
	var size int64
	if l.active != nil && id == l.active.id {
		f, size = l.active.f, l.active.end
	} else {
		if !l.pin(id) {
			return record3{}, errSegGone
		}
		defer l.unpin(id)
		if f, err = l.fs.OpenFile(filepath.Join(l.dir, segName(id)), os.O_RDONLY, 0); err != nil {
			return record3{}, err
		}
		defer f.Close()
		if size, err = f.Size(); err != nil {
			return record3{}, err
		}
	}
	fr := &frameReader{f: f, size: size, chunk: 16 << 10}
	for at := pos; at < size; {
		typ, body, n, err := fr.next(at)
		if err != nil {
			return record3{}, corrupt(id, at, "%v", err)
		}
		if typ == f3Record {
			rec, err := parseRecord3(body)
			if err != nil {
				return record3{}, corrupt(id, at, "%v", err)
			}
			if rec.Partition == pid {
				if rec.Offset == off {
					rec.Key, rec.ID, rec.Value = bytes.Clone(rec.Key), bytes.Clone(rec.ID), bytes.Clone(rec.Value)
					return rec, nil
				}
				if rec.Offset > off {
					break
				}
			}
		}
		at += int64(n)
	}
	return record3{}, errSegNotFound
}

// findIDs visits every recorded offset whose hash matches (pid, id) in the active segment and then
// in the previous window sealed segments, newest first, until visit returns true. A match is only a
// candidate: the caller confirms it against the log (compare the frame's ID, key and value).
func (l *segLog) findIDs(pid uint32, id []byte, window int, visit func(seg, off uint64, index *segIndex) bool) error {
	h := idHash(pid, id)
	stop := false
	if l.idx != nil {
		l.idx.ids.lookup(h, func(off uint64) bool { stop = visit(l.active.id, off, l.idx); return stop })
		if stop {
			return nil
		}
	}
	// Public publishers hold Node.mu for this lookup and for rollover/retirement.
	var ids []uint64
	var ordered []idFilter
	mixed := mixFilterHash(h)
	if l.filters != nil {
		ids = l.ids
		ordered = l.filters.orderedFilters(ids)
	} else {
		l.mu.Lock()
		ids = slices.Clone(l.ids)
		l.mu.Unlock()
	}
	for k := len(ids) - 1; k >= 0 && k >= len(ids)-window; k-- {
		if ordered != nil {
			if !ordered[k].contains(mixed) {
				continue
			}
		} else if l.filters != nil {
			f, ok := l.filters.entries[ids[k]]
			if !ok {
				if loaded, err := l.readFilter(ids[k]); err == nil {
					f = loaded
					ok = true
					l.filters.put(ids[k], loaded)
				}
			}
			if ok && !f.contains(mixed) {
				continue
			}
		}
		l.idProbes++
		x, err := l.sealedIndex(ids[k])
		if err != nil {
			return err
		}
		if l.filters != nil {
			if _, exists := l.filters.entries[ids[k]]; !exists {
				l.rememberFilter(ids[k], x)
			}
		}
		x.ids.lookup(h, func(off uint64) bool { stop = visit(ids[k], off, x); return stop })
		if stop {
			return nil
		}
	}
	return nil
}
