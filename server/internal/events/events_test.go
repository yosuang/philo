package events

import "testing"

func TestWithTypeFlattensPayload(t *testing.T) {
	got, err := WithType("approval.requested", map[string]any{
		"id":     "a",
		"status": "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "approval.requested" || got["id"] != "a" || got["status"] != "pending" {
		t.Fatalf("%#v", got)
	}
}
