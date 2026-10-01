package recording

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func now() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

func TestSessionDurabilityAndVerification(t *testing.T) {
	root := t.TempDir()
	session, err := Create(root, "BTC/USD", 10, now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(root, "BTC/USD", 10, now()); err == nil {
		t.Fatal("second capture acquired the same root lock")
	}
	if err := session.StartSegment(now(), 0); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"channel":"book","data":[{"price":1.2300e-3}]}`)
	if err := session.AppendFrame(payload, now().Add(time.Millisecond), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if session.Snapshot().WrittenSeq != 2 || session.Snapshot().DurableSeq != 0 {
		t.Fatal("written position was confused with durable position")
	}
	if err := session.Sync(now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if session.Snapshot().DurableSeq != 2 {
		t.Fatal("sync did not advance durable position")
	}
	manifest, err := session.Close(now().Add(2*time.Second), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != Complete || manifest.WrittenSeq != 3 || manifest.DurableSeq != 3 || manifest.DataSHA256 == "" {
		t.Fatalf("final manifest = %+v", manifest)
	}
	if _, err := Verify(root, manifest.SessionID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(session.Directory(), "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var envelope Envelope
	if err := json.Unmarshal([]byte(lines[1]), &envelope); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.PayloadB64)
	if err != nil || string(decoded) != string(payload) {
		t.Fatalf("raw payload changed: %s, %v", decoded, err)
	}
	if _, err := SessionDirectory(root, "../../other"); err == nil {
		t.Fatal("path-like session ID was accepted")
	}
	changed := strings.Replace(string(data), "graceful_stop", "graceful_stoq", 1)
	if err := os.WriteFile(filepath.Join(session.Directory(), "frames.jsonl"), []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root, manifest.SessionID); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("content change returned %v", err)
	}
}

func TestRecoveryPreservesTruncatedTailAndRejectsInteriorDamage(t *testing.T) {
	root := t.TempDir()
	session, err := Create(root, "BTC/USD", 10, now())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.StartSegment(now(), 0); err != nil {
		t.Fatal(err)
	}
	if err := session.AppendFrame([]byte(`{"channel":"heartbeat"}`), now(), 0); err != nil {
		t.Fatal(err)
	}
	if err := session.Sync(now()); err != nil {
		t.Fatal(err)
	}
	session.file.Close()
	session.lock.Close()
	framesPath := filepath.Join(session.Directory(), "frames.jsonl")
	file, err := os.OpenFile(framesPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"schema_version":1,"seq":3`); err != nil {
		t.Fatal(err)
	}
	file.Close()
	manifest, err := Recover(root, session.Snapshot().SessionID, now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != Interrupted || manifest.WrittenSeq != 2 || manifest.DurableSeq != 2 || len(manifest.Audit) == 0 {
		t.Fatalf("recovered manifest = %+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(session.Directory(), "truncated-tail.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root, manifest.SessionID); err != nil {
		t.Fatal(err)
	}
	again, err := Recover(root, manifest.SessionID, now().Add(2*time.Second))
	if err != nil || len(again.Audit) != len(manifest.Audit) {
		t.Fatalf("second recovery changed the audit: %+v, %v", again.Audit, err)
	}
	data, _ := os.ReadFile(framesPath)
	data[10] = 'X'
	if err := os.WriteFile(framesPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root, manifest.SessionID); err == nil {
		t.Fatal("interior damage passed verification")
	}
	corrupt, err := Recover(root, manifest.SessionID, now().Add(2*time.Second))
	if err == nil || corrupt.Status != Corrupt {
		t.Fatalf("interior damage recovery = %+v, %v", corrupt, err)
	}
}

type failingSyncFile struct{ frameFile }

func (failingSyncFile) Sync() error { return errors.New("injected disk sync error") }

func TestSyncFailureCannotCompleteRecording(t *testing.T) {
	root := t.TempDir()
	session, err := Create(root, "BTC/USD", 10, now())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.StartSegment(now(), 0); err != nil {
		t.Fatal(err)
	}
	session.file = failingSyncFile{session.file}
	if err := session.Sync(now()); err == nil {
		t.Fatal("injected sync failure was ignored")
	}
	manifest, _ := session.Abort("durability_failed", now())
	if manifest.Status != Corrupt || manifest.DurableSeq != 0 {
		t.Fatalf("failed recording was promoted: %+v", manifest)
	}
}

func TestNewCaptureRecoversAnOpenSession(t *testing.T) {
	root := t.TempDir()
	old, err := Create(root, "BTC/USD", 10, now())
	if err != nil {
		t.Fatal(err)
	}
	if err := old.StartSegment(now(), 0); err != nil {
		t.Fatal(err)
	}
	if err := old.Sync(now()); err != nil {
		t.Fatal(err)
	}
	old.file.Close()
	old.lock.Close()
	fresh, err := Create(root, "BTC/USD", 10, now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Abort("test_end", now().Add(time.Minute))
	recovered, err := LoadManifest(root, old.Snapshot().SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != Interrupted || recovered.Segments[0].EndReason != "recovered_interruption" {
		t.Fatalf("startup recovery = %+v", recovered)
	}
}
