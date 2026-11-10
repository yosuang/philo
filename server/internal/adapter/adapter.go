// Package adapter is the model boundary. The dev and provider implementations
// do not import each other. Provider mode must never call the dev adapter.
package adapter

import "context"

// ApprovalSpec is an in-server draft the adapter wants stored after a
// successful stream. The engine creates it. Adapters do not write to the database.
type ApprovalSpec struct {
	Kind    string
	Summary string
}

// Request is one user turn. History is not replayed into the dev adapter.
type Request struct {
	ModelID      string
	Instructions string
	UserContent  string
}

// Result is the outcome of a finished stream.
type Result struct {
	Approval *ApprovalSpec
}

// Adapter streams a reply. emit receives incremental text, not the full body.
// A canceled context must stop the stream and return ctx.Err().
type Adapter interface {
	Name() string
	Stream(ctx context.Context, req Request, emit func(text string) error) (Result, error)
}

// Failure is a generation error that must be surfaced, not swallowed.
type Failure struct {
	Code    string
	Message string
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	if f.Message == "" {
		return f.Code
	}
	return f.Message
}
