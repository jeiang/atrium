package permission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/tool"
)

const Kind = "permission"

type Payload struct {
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args"`
	ThreadID int64           `json:"thread_id"`
	RunID    int64           `json:"run_id"`
	RunKind  tool.Kind       `json:"run_kind"`
}

type Preview struct {
	Args tool.Args `json:"args"`
}

func NewPayload(name string, tc *tool.Context, args tool.Args) (Payload, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return Payload{}, err
	}
	return Payload{Tool: name, Args: raw, ThreadID: tc.ThreadID, RunID: tc.RunID, RunKind: tc.Kind}, nil
}

func Fingerprint(name string, runID int64, args json.RawMessage) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%s", Kind, name, runID, args))
	return hex.EncodeToString(sum[:])
}

func FingerprintOf(name string, runID int64, args tool.Args) (string, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return Fingerprint(name, runID, raw), nil
}

func Title(name string) string { return "Allow " + name }
