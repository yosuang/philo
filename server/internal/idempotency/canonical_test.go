package idempotency

import "testing"

func TestDraftVectors(t *testing.T) {
	hello, err := Hash(SubmitPayload("Hello", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if hello != "71dd282e23b7855dc47642140331f2a9bbca47c5e6845bd30030b9dc64919037" {
		t.Fatalf("hello hash = %s", hello)
	}
	spaced, err := Hash(SubmitPayload("Hello ", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if spaced != "5c1ad94f5b30c81fc1e9e24aee3b654af1ce1fe3eed8a9893955c40a1e359265" {
		t.Fatalf("spaced hash = %s", spaced)
	}
	reversed, err := Hash(map[string]any{"options": map[string]any{}, "content": "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if reversed != hello {
		t.Fatalf("key order changed the hash: %s", reversed)
	}
	approval, err := Hash(map[string]any{
		"snapshot_hash": "abc",
		"decision":      "approve",
		"approval_id":   "ap1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if approval != "daecd54f9c904e1ae0e415efbec7983dbbb21edfa315e97fb496901a4451e0c4" {
		t.Fatalf("approval hash = %s", approval)
	}
}
