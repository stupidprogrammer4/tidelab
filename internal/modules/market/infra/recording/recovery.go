package recording

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"
)

type scanResult struct {
	seq       uint64
	goodBytes int64
	lastNS    int64
	tail      []byte
	hash      string
	active    bool
	segmentID uint64
	segments  []Segment
}

func recoverOpenSessions(root string, now time.Time) error {
	entries, err := os.ReadDir(filepath.Join(root, "sessions"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !sessionIDPattern.MatchString(entry.Name()) {
			continue
		}
		manifest, err := LoadManifest(root, entry.Name())
		if err != nil {
			return err
		}
		if manifest.Status == Open {
			if _, err := Recover(root, entry.Name(), now); err != nil {
				return err
			}
		}
	}
	return nil
}

// Verify checks the stored receive order and content hash without changing files.
func Verify(root, id string) (Manifest, error) {
	manifest, err := LoadManifest(root, id)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Status == Corrupt {
		return manifest, errors.New("recording is marked corrupt")
	}
	dir, _ := SessionDirectory(root, id)
	scan, err := scanFrames(dir, manifest)
	if err != nil {
		return manifest, err
	}
	if len(scan.tail) != 0 {
		return manifest, errors.New("truncated final JSONL line")
	}
	if manifest.DataSHA256 != "" && manifest.DataSHA256 != scan.hash {
		return manifest, errors.New("recording content hash mismatch")
	}
	if manifest.DurableSeq > scan.seq || manifest.WrittenSeq > scan.seq {
		return manifest, errors.New("manifest positions exceed the recorded prefix")
	}
	if manifest.Status == Complete && (manifest.DurableSeq != scan.seq || manifest.WrittenSeq != scan.seq || scan.active || manifest.DataSHA256 == "") {
		return manifest, errors.New("complete manifest does not match committed frame sequence")
	}
	if manifest.Status == Complete && !reflect.DeepEqual(manifest.Segments, scan.segments) {
		return manifest, errors.New("complete manifest segment summaries do not match frames")
	}
	return manifest, nil
}

// Recover turns an open recording into an audited interrupted prefix. It only
// trims a truncated final line; any interior damage or hash mismatch is corrupt.
func Recover(root, id string, now time.Time) (Manifest, error) {
	manifest, err := LoadManifest(root, id)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Status == Complete || manifest.Status == Corrupt {
		return manifest, errors.New("only open or interrupted recordings can be recovered")
	}
	dir, _ := SessionDirectory(root, id)
	scan, err := scanFrames(dir, manifest)
	if err != nil {
		return markCorrupt(dir, manifest, "recovery_validation_failed: "+err.Error())
	}
	if manifest.DataSHA256 != "" && manifest.DataSHA256 != scan.hash {
		return markCorrupt(dir, manifest, "recovery_hash_mismatch")
	}
	if manifest.DurableSeq > scan.seq || manifest.WrittenSeq > scan.seq {
		return markCorrupt(dir, manifest, "recovery_position_mismatch")
	}
	if manifest.Status == Interrupted && len(scan.tail) == 0 && manifest.DataSHA256 == scan.hash &&
		manifest.WrittenSeq == scan.seq && manifest.DurableSeq == scan.seq {
		return manifest, nil
	}
	for _, change := range manifest.InstrumentHistory {
		if change.AtSeq > scan.seq {
			return markCorrupt(dir, manifest, "recovery_instrument_position_mismatch")
		}
	}
	if len(scan.tail) != 0 {
		tailPath := filepath.Join(dir, "truncated-tail.bin")
		if existing, err := os.ReadFile(tailPath); err == nil {
			if !bytes.Equal(existing, scan.tail) {
				return markCorrupt(dir, manifest, "recovery_tail_evidence_mismatch")
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if err := copyFile(tailPath, scan.tail); err != nil {
				return manifest, err
			}
		} else {
			return manifest, err
		}
		file, err := os.OpenFile(filepath.Join(dir, "frames.jsonl"), os.O_RDWR, 0600)
		if err != nil {
			return manifest, err
		}
		if err := file.Truncate(scan.goodBytes); err != nil {
			file.Close()
			return manifest, err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return manifest, err
		}
		file.Close()
		manifest.Audit = append(manifest.Audit, fmt.Sprintf("recovered truncated final line of %d bytes; original saved as truncated-tail.bin", len(scan.tail)))
	} else {
		manifest.Audit = append(manifest.Audit, "recovered clean JSONL prefix from unfinished session")
	}
	manifest.Status = Interrupted
	manifest.WrittenSeq = scan.seq
	manifest.DurableSeq = scan.seq
	manifest.DataSHA256 = scan.hash
	manifest.Segments = scan.segments
	ended := now.UTC()
	manifest.EndedAt = &ended
	if scan.active && len(manifest.Segments) > 0 {
		last := &manifest.Segments[len(manifest.Segments)-1]
		last.EndSeq = scan.seq
		last.EndedAt = &ended
		last.EndReason = "recovered_interruption"
	}
	writer := &Session{dir: dir}
	if err := writer.writeManifest(manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func markCorrupt(dir string, manifest Manifest, reason string) (Manifest, error) {
	manifest.Status = Corrupt
	manifest.Audit = append(manifest.Audit, reason)
	if err := (&Session{dir: dir}).writeManifest(manifest); err != nil {
		return manifest, err
	}
	return manifest, errors.New(reason)
}

func scanFrames(dir string, manifest Manifest) (scanResult, error) {
	file, err := os.Open(filepath.Join(dir, "frames.jsonl"))
	if err != nil {
		return scanResult{}, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 2<<20)
	hasher := sha256.New()
	result := scanResult{}
	for {
		line, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			return result, errors.New("recording line exceeds 2 MiB")
		}
		if readErr == io.EOF {
			result.tail = append([]byte(nil), line...)
			break
		}
		if readErr != nil {
			return result, readErr
		}
		var entry Envelope
		if err := json.Unmarshal(line, &entry); err != nil {
			return result, fmt.Errorf("invalid JSONL line at seq %d: %w", result.seq+1, err)
		}
		if entry.SchemaVersion != SchemaVersion || entry.SessionID != manifest.SessionID || entry.Seq != result.seq+1 {
			return result, fmt.Errorf("noncontiguous or mismatched envelope at seq %d", result.seq+1)
		}
		offset, err := strconv.ParseInt(entry.ReceiveOffsetNS, 10, 64)
		if err != nil || offset < 0 || offset < result.lastNS || entry.ReceivedAt.IsZero() {
			return result, fmt.Errorf("invalid receive offset at seq %d", entry.Seq)
		}
		switch entry.Kind {
		case "segment_start":
			if result.active || entry.SegmentID != result.segmentID+1 || entry.PayloadB64 != "" || entry.Reason != "" {
				return result, fmt.Errorf("invalid segment start at seq %d", entry.Seq)
			}
			result.active = true
			result.segmentID++
			result.segments = append(result.segments, Segment{ID: entry.SegmentID, StartSeq: entry.Seq, StartedAt: entry.ReceivedAt})
		case "segment_end":
			if !result.active || entry.SegmentID != result.segmentID || entry.PayloadB64 != "" || entry.Reason == "" {
				return result, fmt.Errorf("invalid segment end at seq %d", entry.Seq)
			}
			result.active = false
			segment := &result.segments[len(result.segments)-1]
			segment.EndSeq = entry.Seq
			ended := entry.ReceivedAt
			segment.EndedAt = &ended
			segment.EndReason = entry.Reason
		case "ws_frame":
			if !result.active || entry.SegmentID != result.segmentID || entry.PayloadB64 == "" || entry.Reason != "" {
				return result, fmt.Errorf("frame outside segment at seq %d", entry.Seq)
			}
			payload, err := base64.StdEncoding.DecodeString(entry.PayloadB64)
			if err != nil || len(payload) == 0 || len(payload) > MaxFrameBytes {
				return result, fmt.Errorf("invalid base64 frame at seq %d", entry.Seq)
			}
		default:
			return result, fmt.Errorf("unknown envelope kind at seq %d", entry.Seq)
		}
		if _, err := hasher.Write(line); err != nil {
			return result, err
		}
		result.seq++
		result.goodBytes += int64(len(line))
		result.lastNS = offset
	}
	result.hash = hex.EncodeToString(hasher.Sum(nil))
	return result, nil
}
