package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestCapturedBytesMatchTheHandoff(t *testing.T) {
	got := CapturedNoKeyFailure()
	// Hashes of the capture lines, without the file's trailing newline.
	// public log: makez-philo-03-replay-cursor.txt line 14
	// task GET: makez-philo-04-terminal-status.txt line 18
	if sha(bytes.TrimRight(got.PublicLog, "\n")) != "4b78068513cc2f8537fa8ea5e33b8d3332712ea54187e1b465e38859ae72b0ae" {
		t.Fatalf("public log sha = %s", sha(bytes.TrimRight(got.PublicLog, "\n")))
	}
	if sha(bytes.TrimRight(got.TaskGET, "\n")) != "f5cf842dcdc1f30511e6e237c22d417d1423b0ab32a582def02b5f0abe10b456" {
		t.Fatalf("task get sha = %s", sha(bytes.TrimRight(got.TaskGET, "\n")))
	}
	for _, raw := range [][]byte{got.PublicLog, got.TaskGET, got.SSEAfter0} {
		if bytes.Contains(raw, []byte("source_offset")) {
			t.Fatal("capture bytes contain source_offset")
		}
	}
	if bytes.Contains(got.PublicLog, []byte("agent_end")) || bytes.Contains(got.PublicLog, []byte("agent_settled")) || bytes.Contains(got.PublicLog, []byte("text_delta")) {
		t.Fatal("capture log contains a settled or delta event")
	}

	frames, err := ParseSSE(got.SSEAfter0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %#v", frames)
	}
	if frames[0].ID != "1" || frames[0].Event != "log" {
		t.Fatalf("log frame = %#v", frames[0])
	}
	if frames[1].ID != "" || frames[1].Event != "status" {
		t.Fatalf("status frame id = %q event = %q", frames[1].ID, frames[1].Event)
	}
	if !bytes.Equal(bytes.TrimSpace(frames[1].Data), bytes.TrimSpace(got.TaskGET)) {
		t.Fatal("status data is not the task JSON")
	}
	var status map[string]any
	if err := json.Unmarshal(frames[1].Data, &status); err != nil {
		t.Fatal(err)
	}
	if status["id"] != capturedTaskID {
		t.Fatalf("status json id = %#v", status["id"])
	}

	last, err := ParseSSE(got.SSELastEvent)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 1 || last[0].ID != "" || last[0].Event != "status" {
		t.Fatalf("last-event frames = %#v", last)
	}

	logs, err := parsePublicLogs(frames[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Seq != 1 || logs[0].RunID != capturedRunID || logs[0].Stream != "stdout" {
		t.Fatalf("log = %#v", logs)
	}
	if !strings.HasPrefix(logs[0].FailureText, "No API key found for the selected model.") {
		t.Fatalf("failure text = %q", logs[0].FailureText)
	}

	task, err := parseTask(got.TaskGET)
	if err != nil {
		t.Fatal(err)
	}
	if !task.capturedFailure() {
		t.Fatalf("task = %#v", task)
	}
	if task.Cursor != "stdout:378,stderr:0" {
		t.Fatalf("cursor = %q", task.Cursor)
	}
	if task.Cursor == "1" || logs[0].Seq == 378 {
		t.Fatal("process cursor was used as the public seq")
	}

	preparing, err := parseTask(got.TaskPreparing)
	if err != nil {
		t.Fatal(err)
	}
	if preparing.capturedFailure() || preparing.State != "RUNNING" || preparing.RunState != "PREPARING" {
		t.Fatalf("preparing = %#v", preparing)
	}
}

func sha(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
