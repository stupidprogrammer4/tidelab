package recording

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
)

const (
	SchemaVersion  = 1
	AdapterVersion = "kraken-ws-v2-book-v1"
	MaxFrameBytes  = 1 << 20
	SyncBytes      = 1 << 20
	SyncInterval   = time.Second
)

type Status string

const (
	Open        Status = "open"
	Complete    Status = "complete"
	Interrupted Status = "interrupted"
	Corrupt     Status = "corrupt"
)

type Envelope struct {
	SchemaVersion   int       `json:"schema_version"`
	SessionID       string    `json:"session_id"`
	Seq             uint64    `json:"seq"`
	SegmentID       uint64    `json:"segment_id"`
	ReceiveOffsetNS string    `json:"receive_offset_ns"`
	ReceivedAt      time.Time `json:"received_at"`
	Kind            string    `json:"kind"`
	PayloadB64      string    `json:"payload_b64,omitempty"`
	Reason          string    `json:"reason,omitempty"`
}

type Segment struct {
	ID        uint64     `json:"id"`
	StartSeq  uint64     `json:"start_seq"`
	EndSeq    uint64     `json:"end_seq"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	EndReason string     `json:"end_reason"`
}

type InstrumentChange struct {
	AtSeq      uint64            `json:"at_seq"`
	Instrument domain.Instrument `json:"instrument"`
}

type Manifest struct {
	SchemaVersion     int                `json:"schema_version"`
	AdapterVersion    string             `json:"adapter_version"`
	SessionID         string             `json:"session_id"`
	Exchange          string             `json:"exchange"`
	Symbol            string             `json:"symbol"`
	SubscribedDepth   int                `json:"subscribed_depth"`
	StartedAt         time.Time          `json:"started_at"`
	EndedAt           *time.Time         `json:"ended_at"`
	Status            Status             `json:"status"`
	InstrumentHistory []InstrumentChange `json:"instrument_history"`
	Segments          []Segment          `json:"segments"`
	WrittenSeq        uint64             `json:"written_seq"`
	DurableSeq        uint64             `json:"durable_seq"`
	DataSHA256        string             `json:"data_sha256"`
	Audit             []string           `json:"audit"`
}

type frameFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type Session struct {
	dir            string
	file           frameFile
	lock           *os.File
	manifest       Manifest
	hash           hash.Hash
	lastOffset     time.Duration
	lastSync       time.Time
	bytesSinceSync int
	activeSegment  bool
	failed         bool
	closed         bool
}

var sessionIDPattern = regexp.MustCompile(`^session_[0-9a-f]{32}$`)

func Create(root, symbol string, depth int, at time.Time) (*Session, error) {
	if root == "" || symbol == "" || depth < 1 || depth > 1000 || at.IsZero() {
		return nil, errors.New("recording root, symbol, depth, and start time are required")
	}
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(root, "capture.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another capture owns the data root")
	}
	if err := recoverOpenSessions(root, at); err != nil {
		lock.Close()
		return nil, fmt.Errorf("recover previous capture: %w", err)
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		lock.Close()
		return nil, err
	}
	id := "session_" + hex.EncodeToString(idBytes)
	dir := filepath.Join(root, "sessions", id)
	if err := os.Mkdir(dir, 0700); err != nil {
		lock.Close()
		return nil, err
	}
	parent, err := os.Open(filepath.Join(root, "sessions"))
	if err != nil {
		os.Remove(dir)
		lock.Close()
		return nil, err
	}
	if err := parent.Sync(); err != nil {
		parent.Close()
		os.Remove(dir)
		lock.Close()
		return nil, err
	}
	parent.Close()
	file, err := os.OpenFile(filepath.Join(dir, "frames.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.Remove(dir)
		lock.Close()
		return nil, err
	}
	session := &Session{
		dir: dir, file: file, lock: lock, hash: sha256.New(), lastSync: at,
		manifest: Manifest{
			SchemaVersion: SchemaVersion, AdapterVersion: AdapterVersion,
			SessionID: id, Exchange: "kraken_spot", Symbol: symbol,
			SubscribedDepth: depth, StartedAt: at.UTC(), Status: Open,
			InstrumentHistory: []InstrumentChange{}, Segments: []Segment{}, Audit: []string{},
		},
	}
	if err := session.writeManifest(session.manifest); err != nil {
		file.Close()
		os.RemoveAll(dir)
		lock.Close()
		return nil, err
	}
	return session, nil
}

func (session *Session) Snapshot() Manifest {
	copy := session.manifest
	copy.InstrumentHistory = append([]InstrumentChange(nil), copy.InstrumentHistory...)
	copy.Segments = append([]Segment(nil), copy.Segments...)
	copy.Audit = append([]string(nil), copy.Audit...)
	return copy
}

func (session *Session) Directory() string { return session.dir }

func (session *Session) SetInstrument(instrument domain.Instrument) error {
	if session.closed || session.failed {
		return errors.New("recording is closed or failed")
	}
	if err := instrument.Validate(); err != nil {
		return err
	}
	session.manifest.InstrumentHistory = append(session.manifest.InstrumentHistory,
		InstrumentChange{AtSeq: session.manifest.WrittenSeq, Instrument: instrument})
	if err := session.writeManifest(session.manifest); err != nil {
		session.failed = true
		return err
	}
	return nil
}

func (session *Session) StartSegment(at time.Time, offset time.Duration) error {
	if session.activeSegment {
		return errors.New("segment already active")
	}
	id := uint64(len(session.manifest.Segments) + 1)
	if err := session.append("segment_start", id, nil, "", at, offset); err != nil {
		return err
	}
	session.manifest.Segments = append(session.manifest.Segments, Segment{
		ID: id, StartSeq: session.manifest.WrittenSeq, StartedAt: at.UTC(),
	})
	session.activeSegment = true
	return nil
}

func (session *Session) AppendFrame(payload []byte, at time.Time, offset time.Duration) error {
	if !session.activeSegment || len(payload) == 0 || len(payload) > MaxFrameBytes {
		return errors.New("active segment and a WebSocket frame within 1 MiB are required")
	}
	id := session.manifest.Segments[len(session.manifest.Segments)-1].ID
	return session.append("ws_frame", id, payload, "", at, offset)
}

func (session *Session) EndSegment(reason string, at time.Time, offset time.Duration) error {
	if !session.activeSegment || reason == "" {
		return errors.New("active segment and end reason are required")
	}
	index := len(session.manifest.Segments) - 1
	id := session.manifest.Segments[index].ID
	if err := session.append("segment_end", id, nil, reason, at, offset); err != nil {
		return err
	}
	segment := &session.manifest.Segments[index]
	segment.EndSeq = session.manifest.WrittenSeq
	ended := at.UTC()
	segment.EndedAt = &ended
	segment.EndReason = reason
	session.activeSegment = false
	return nil
}

func (session *Session) append(kind string, segmentID uint64, payload []byte, reason string, at time.Time, offset time.Duration) error {
	if session.closed || session.failed {
		return errors.New("recording is closed or failed")
	}
	if offset < 0 || offset < session.lastOffset || at.IsZero() {
		return errors.New("receive offsets must be nonnegative and nondecreasing")
	}
	envelope := Envelope{
		SchemaVersion: SchemaVersion, SessionID: session.manifest.SessionID,
		Seq: session.manifest.WrittenSeq + 1, SegmentID: segmentID,
		ReceiveOffsetNS: fmt.Sprint(offset.Nanoseconds()), ReceivedAt: at.UTC(),
		Kind: kind, Reason: reason,
	}
	if kind == "ws_frame" {
		envelope.PayloadB64 = base64.StdEncoding.EncodeToString(payload)
	}
	line, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	written, err := session.file.Write(line)
	if err != nil || written != len(line) {
		session.failed = true
		return fmt.Errorf("write recording frame: %w", firstWriteError(err, written, len(line)))
	}
	if _, err := session.hash.Write(line); err != nil {
		session.failed = true
		return err
	}
	session.manifest.WrittenSeq++
	session.lastOffset = offset
	session.bytesSinceSync += written
	return nil
}

func firstWriteError(err error, written, expected int) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("short write: %d of %d bytes", written, expected)
}

func (session *Session) SyncIfDue(now time.Time) error {
	if session.bytesSinceSync >= SyncBytes || now.Sub(session.lastSync) >= SyncInterval {
		return session.Sync(now)
	}
	return nil
}

func (session *Session) Sync(now time.Time) error {
	if session.closed || session.failed {
		return errors.New("recording is closed or failed")
	}
	if err := session.file.Sync(); err != nil {
		session.failed = true
		return fmt.Errorf("sync recording frames: %w", err)
	}
	copy := session.manifest
	copy.DurableSeq = copy.WrittenSeq
	if err := session.writeManifest(copy); err != nil {
		session.failed = true
		return err
	}
	session.manifest = copy
	session.lastSync = now
	session.bytesSinceSync = 0
	return nil
}

func (session *Session) Close(at time.Time, offset time.Duration) (Manifest, error) {
	if session.closed {
		return session.Snapshot(), errors.New("recording already closed")
	}
	if session.activeSegment {
		if err := session.EndSegment("graceful_stop", at, offset); err != nil {
			session.Abort("segment_close_failed", at)
			return session.Snapshot(), err
		}
	}
	if err := session.Sync(at); err != nil {
		session.Abort("durability_failed", at)
		return session.Snapshot(), err
	}
	copy := session.manifest
	copy.Status = Complete
	ended := at.UTC()
	copy.EndedAt = &ended
	copy.DataSHA256 = hex.EncodeToString(session.hash.Sum(nil))
	if err := session.writeManifest(copy); err != nil {
		session.Abort("manifest_finalize_failed", at)
		return session.Snapshot(), err
	}
	session.manifest = copy
	session.closed = true
	err := session.file.Close()
	session.lock.Close()
	return session.Snapshot(), err
}

func (session *Session) Abort(reason string, at time.Time) (Manifest, error) {
	if session.closed {
		return session.Snapshot(), nil
	}
	copy := session.manifest
	copy.Status = Interrupted
	if session.failed {
		copy.Status = Corrupt
	}
	ended := at.UTC()
	copy.EndedAt = &ended
	copy.Audit = append(copy.Audit, reason)
	if !session.failed {
		if err := session.file.Sync(); err == nil {
			copy.DurableSeq = copy.WrittenSeq
			copy.DataSHA256 = hex.EncodeToString(session.hash.Sum(nil))
		} else {
			copy.Status = Corrupt
			copy.Audit = append(copy.Audit, "frame_sync_failed: "+err.Error())
		}
	}
	err := session.writeManifest(copy)
	session.manifest = copy
	session.closed = true
	if closeErr := session.file.Close(); err == nil {
		err = closeErr
	}
	session.lock.Close()
	return session.Snapshot(), err
}

func (session *Session) writeManifest(manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(session.dir, ".manifest-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, filepath.Join(session.dir, "manifest.json")); err != nil {
		return err
	}
	dir, err := os.Open(session.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func SessionDirectory(root, id string) (string, error) {
	if !sessionIDPattern.MatchString(id) {
		return "", errors.New("invalid session ID")
	}
	return filepath.Join(root, "sessions", id), nil
}

func LoadManifest(root, id string) (Manifest, error) {
	dir, err := SessionDirectory(root, id)
	if err != nil {
		return Manifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.SessionID != id {
		return Manifest{}, errors.New("manifest schema or session ID mismatch")
	}
	return manifest, nil
}

func copyFile(destination string, source []byte) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if written, err := file.Write(source); err != nil || written != len(source) {
		file.Close()
		return firstWriteError(err, written, len(source))
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
