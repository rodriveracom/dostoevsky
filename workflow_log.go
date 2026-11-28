package dostoevsky

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/rodriveracom/dostoevsky/internal/seam"
)

// The witness replaces journal.v1 by rename while the directory's journal.lock
// remains held. Older segmented readers reject the new journal signature.
const workflowMagic = "DOST-WORKFLOW-02\n"
const witnessSize = len(workflowMagic) + 16 + 8 + 8 + 32 + 32

type workflowBoundary struct {
	generation [16]byte
	segment    uint64
	end        int64
	digest     [32]byte
}

func (w workflowBoundary) encode() []byte {
	b := append([]byte(workflowMagic), w.generation[:]...)
	b = binary.LittleEndian.AppendUint64(b, w.segment)
	b = binary.LittleEndian.AppendUint64(b, uint64(w.end))
	b = append(b, w.digest[:]...)
	sum := sha256.Sum256(b)
	return append(b, sum[:]...)
}
func decodeBoundary(b []byte) (workflowBoundary, error) {
	var w workflowBoundary
	if len(b) != witnessSize || !bytes.HasPrefix(b, []byte(workflowMagic)) {
		return w, ErrCorruptLog
	}
	end := len(b) - 32
	sum := sha256.Sum256(b[:end])
	if !bytes.Equal(sum[:], b[end:]) {
		return w, ErrCorruptLog
	}
	p := b[len(workflowMagic):]
	copy(w.generation[:], p[:16])
	w.segment = binary.LittleEndian.Uint64(p[16:])
	w.end = int64(binary.LittleEndian.Uint64(p[24:]))
	copy(w.digest[:], p[32:64])
	if w.segment == 0 || w.end < segHeaderSize {
		return w, ErrCorruptLog
	}
	return w, nil
}

// WorkflowLog is the append-only, single-partition storage used by package
// workflow. It has no retention or general-purpose node write methods.
type WorkflowLog struct {
	n       *Node
	w       workflowBoundary
	digest  hash.Hash
	scratch [32 << 10]byte
	persist func(string, []byte) error
}

func publishWitness(dir string, data []byte) error {
	return replaceWorkflowMetadata(dir, "journal.v1", data)
}

func replaceWorkflowMetadata(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, "."+name+".workflow-tmp")
	f, e := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, filepath.Join(dir, name)); e != nil {
		return e
	}
	return syncDir(dir)
}

// OpenWorkflowLog verifies an existing witness before permitting any tail
// repair. Pre-witness stores are adopted only after a read-only frame scan.
func OpenWorkflowLog(dir string) (*WorkflowLog, error) { return OpenWorkflowLogReplaying(dir, nil) }

// OpenWorkflowLogReplaying is OpenWorkflowLog with the caller's replay, which runs
// before a pre-witness directory is converted: if replay returns an error, the older
// format is left as it was. replay may read the log and must not publish to it.
func OpenWorkflowLogReplaying(dir string, replay func(*WorkflowLog) error) (*WorkflowLog, error) {
	j, e := openJournal(dir)
	if e != nil {
		return nil, e
	}
	cfg := Config{Node: "workflow", Nodes: []string{"workflow"}, Topics: map[string]int{"transitions": 1}}
	n := &Node{j: j, cfg: cfg, streams: make(map[Partition]*stream)}
	l := &WorkflowLog{n: n, digest: sha256.New(), persist: publishWitness}
	ok := false
	defer func() {
		if !ok {
			n.Close()
		}
	}()
	data, e := io.ReadAll(io.LimitReader(j.file, int64(witnessSize+1)))
	if e != nil {
		return nil, e
	}
	if partialMarker(dir, data) {
		data = nil // a torn first write of the marker; openSegments decides what may be removed
	}
	witnessed := bytes.HasPrefix(data, []byte(workflowMagic))
	path := filepath.Join(dir, "segments")
	identity, identityErr := readWorkflowID(dir)
	if identityErr != nil && !os.IsNotExist(identityErr) {
		return nil, identityErr
	}
	if identityErr == nil && (len(identity) != 16 || len(data) == 0) {
		return nil, ErrCorruptLog
	}
	if witnessed {
		if l.w, e = decodeBoundary(data); e != nil {
			return nil, e
		}
		if !bytes.Equal(identity, l.w.generation[:]) {
			return nil, ErrCorruptLog
		}
		l.digest.Write(l.w.generation[:])
		if e = l.verifyAndRecover(path); e != nil {
			return nil, e
		}
	} else {
		if len(data) > 0 {
			if string(data) != segmentMarker {
				return nil, ErrCorruptLog
			}
			if e = verifyLegacyWorkflow(path); e != nil {
				return nil, e
			}
		}
		if identityErr == nil {
			copy(l.w.generation[:], identity)
		} else if _, e = seam.Random(l.w.generation[:]); e != nil {
			return nil, e
		}
		l.digest.Write(l.w.generation[:])
	}
	raw, _ := json.Marshal(cfg)
	if _, e = n.openSegments(dir, raw, true, true); e != nil {
		return nil, e
	}
	if replay != nil {
		if e = replay(l); e != nil {
			return nil, e
		}
	}
	if !witnessed {
		if identityErr != nil {
			if e = replaceWorkflowMetadata(dir, "WORKFLOW-ID", l.w.generation[:]); e != nil {
				return nil, e
			}
		}
		if e = l.seal(); e != nil {
			return nil, e
		}
	}
	ok = true
	return l, nil
}
func readWorkflowID(dir string) ([]byte, error) {
	f, err := os.Open(filepath.Join(dir, "WORKFLOW-ID"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 17))
}

func verifyLegacyWorkflow(path string) error {
	if e := checkSegmentRange(path); e != nil {
		return e
	}
	b, e := readSegmentRange(path)
	if e != nil {
		return e
	}
	if binary.LittleEndian.Uint64(b) != 1 {
		return ErrCorruptLog
	}
	// Verify even a newly published successor that RANGE has not yet named.
	names, e := os.ReadDir(path)
	if e != nil {
		return e
	}
	for _, entry := range names {
		if id, ok := parseSegName(entry.Name()); ok {
			seg, _, err := scanSegmentCore(osFS{}, path, id, false, nil, nil)
			if err != nil {
				return err
			}
			if err = seg.f.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *WorkflowLog) hashBytes(path string, id uint64, from, end int64) error {
	f, e := os.Open(filepath.Join(path, segName(id)))
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if end < 0 {
		end = info.Size()
	}
	if from < 0 || from > end || end > info.Size() {
		return corrupt(id, end, "committed bytes missing")
	}
	if from == 0 {
		var tag [8]byte
		binary.LittleEndian.PutUint64(tag[:], id)
		l.digest.Write(tag[:])
	}
	for from < end {
		b := l.scratch[:min(int64(len(l.scratch)), end-from)]
		if _, e = f.ReadAt(b, from); e != nil {
			return e
		}
		l.digest.Write(b)
		from += int64(len(b))
	}
	return nil
}

func (l *WorkflowLog) verifyAndRecover(path string) error {
	w := l.w
	// RANGE itself is published atomically. During a roll it can name one
	// successor before the witness moves. Only one group can be in progress.
	b, e := readSegmentRange(path)
	if e != nil {
		return e
	}
	if len(b) != 20 {
		return ErrCorruptLog
	}
	last := binary.LittleEndian.Uint64(b[8:])
	if !bytes.Equal(b, rangeBytes(1, last)) || last < w.segment || last-w.segment > 1 {
		return ErrCorruptLog
	}
	names, e := os.ReadDir(path)
	if e != nil {
		return e
	}
	for _, entry := range names {
		if id, ok := parseSegName(entry.Name()); ok && id > w.segment && id-w.segment > 1 {
			return ErrCorruptLog
		}
	}
	for id := uint64(1); id <= w.segment; id++ {
		end := int64(-1)
		if id == w.segment {
			end = w.end
		}
		if e = l.hashBytes(path, id, 0, end); e != nil {
			return e
		}
	}
	if !bytes.Equal(l.digest.Sum(nil), w.digest[:]) {
		return corrupt(w.segment, w.end, "workflow committed prefix digest mismatch")
	}
	// Reconciliation is restartable: narrow RANGE before removing its former endpoint.
	if last != w.segment {
		tmp := &segLog{dir: path, active: &segment{id: w.segment}}
		if e = writeSegmentRange(tmp, 1); e != nil {
			return e
		}
	}
	dirty := false
	for _, name := range []string{segName(w.segment + 1), idxName(w.segment + 1), filterName(w.segment + 1)} {
		if e = os.Remove(filepath.Join(path, name)); e == nil {
			dirty = true
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	filename := filepath.Join(path, segName(w.segment))
	info, e := os.Stat(filename)
	if e != nil {
		return e
	}
	if info.Size() > w.end {
		f, err := os.OpenFile(filename, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		if err = f.Truncate(w.end); err == nil {
			err = f.Sync()
		}
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
	}
	if dirty {
		return syncDir(path)
	}
	return nil
}

func (l *WorkflowLog) seal() error {
	log := l.n.segments
	w := l.w
	first := max(uint64(1), w.segment)
	for id := first; id <= log.active.id; id++ {
		from, end := int64(0), int64(-1)
		if id == w.segment {
			from = w.end
		}
		if id == log.active.id {
			end = log.active.end
		}
		if e := l.hashBytes(log.dir, id, from, end); e != nil {
			return e
		}
	}
	w.segment, w.end = log.active.id, log.active.end
	copy(w.digest[:], l.digest.Sum(nil))
	if e := l.persist(filepath.Dir(log.dir), w.encode()); e != nil {
		return e
	}
	l.w = w
	return nil
}
func (l *WorkflowLog) PublishBatch(ctx context.Context, items []Publication) ([]Record, error) {
	n := l.n
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(items) == 0 || len(items) > 1024 {
		return nil, ErrInvalid
	}
	if e := seam.At("workflow.before_append"); e != nil {
		n.segments.failed = e
		return nil, e
	}
	r, e := n.publishBatchLocked(ctx, items)
	if e != nil {
		return nil, e
	}
	if e = seam.At("workflow.after_segment_sync_before_witness"); e == nil {
		e = l.seal()
	}
	if e == nil {
		e = seam.At("workflow.after_witness_before_reply")
	}
	if e != nil {
		n.segments.failed = e
		return nil, e
	}
	return r, nil
}

// ReadBatch reads durable workflow records for replay. Only immediate reads are supported.
func (l *WorkflowLog) ReadBatch(ctx context.Context, p Partition, off int64, limits Limits, wait bool) (Page, error) {
	if wait || limits.Records < 1 || limits.Bytes < 1 {
		return Page{}, ErrInvalid
	}
	n := l.n
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.closed {
		return Page{}, ErrClosed
	}
	if e := n.segments.failed; e != nil {
		return Page{}, e
	}
	if e := ctx.Err(); e != nil {
		return Page{}, e
	}
	return n.readSegmentPage(ctx, p, off, limits)
}
func (l *WorkflowLog) Close() error { return l.n.Close() }
