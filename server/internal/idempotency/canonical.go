// Package idempotency hashes the semantic payload for one accept.
// The algorithm is the draft's version 1: SHA-256 of canonical JSON.
// Object keys are sorted at every level. Array order is kept.
// Whitespace and raw key order are not significant.
package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// HashVersion is the semantic-hash version for this document.
// It is not Makez's task-hash version.
const HashVersion = 1

// Hash returns the lowercase hex SHA-256 of the canonical JSON of v.
func Hash(v any) (string, error) {
	body, err := Canonical(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// Canonical encodes v with sorted object keys and no HTML escaping.
func Canonical(v any) ([]byte, error) {
	norm, err := normalize(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(norm); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// SubmitPayload is the version-1 submit document: content and options.
func SubmitPayload(content string, options map[string]any) map[string]any {
	if options == nil {
		options = map[string]any{}
	}
	return map[string]any{"content": content, "options": options}
}

// ParseOptions decodes an options object. Omitted options are an empty object.
// A non-object is rejected. Nested object keys are kept for the hash.
func ParseOptions(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("options must be a JSON object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("options must be a JSON object")
	}
	norm, err := normalize(obj)
	if err != nil {
		return nil, err
	}
	out, ok := norm.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("options must be a JSON object")
	}
	return out, nil
}

func normalize(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for key := range t {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, key := range keys {
			child, err := normalize(t[key])
			if err != nil {
				return nil, err
			}
			out[key] = child
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i := range t {
			child, err := normalize(t[i])
			if err != nil {
				return nil, err
			}
			out[i] = child
		}
		return out, nil
	case json.Number, string, bool, nil, float64, int, int64:
		return t, nil
	default:
		return nil, fmt.Errorf("cannot canonicalize %T", v)
	}
}
