package makez

import "encoding/json"

// Values ParseExecution accepts on Makez be00c8291e5143a9dc966f09813373acb076135e
// (internal/piharness/execution.go). That file calls the contract spelling
// proposed. The live parser rejects any other contract or pin, so a pi@1
// submit has to send these bytes. They are not an owner-to-workspace binding
// and they do not freeze source_offset or retention.
const (
	ExecutionContract = "2026-11-10.1"
	HarnessPin        = "dd30101cfc5b0609411dd80b6f93adbc14db14cf"
	AgentID           = "pi"
	AgentVersion      = "1"
	// TaskTimeoutSeconds is the timeout_seconds the no-key probe sends.
	// The Makez route accepts 5–5400. This is not a retention period.
	TaskTimeoutSeconds = 180
)

type executionEnvelope struct {
	Execution executionBody `json:"execution"`
}

type executionBody struct {
	Contract       string `json:"contract"`
	Scope          string `json:"scope"`
	BotID          string `json:"bot_id"`
	ConversationID string `json:"conversation_id"`
	HarnessPin     string `json:"harness_pin"`
}

func executionConfig(botID, conversationID string) ([]byte, error) {
	return json.Marshal(executionEnvelope{Execution: executionBody{
		Contract:       ExecutionContract,
		Scope:          "personal",
		BotID:          botID,
		ConversationID: conversationID,
		HarnessPin:     HarnessPin,
	}})
}

// SubmitKey is the Makez Idempotency-Key for one durable turn.
// The contract derives it from the turn id and the operation kind,
// not from the client idempotency key. Makez's key space is per workspace.
func SubmitKey(turnID string) (string, error) {
	if turnID == "" || len(turnID) > 100 {
		return "", errIncomplete
	}
	key := "turn/" + turnID + "/submit"
	if len(key) > 128 {
		return "", errIncomplete
	}
	return key, nil
}
