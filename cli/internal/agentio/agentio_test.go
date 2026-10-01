package agentio

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func decodeStrict(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/examples/" + name)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func TestExamplesMatchContract(t *testing.T) {
	var req HealRequest
	decodeStrict(t, "heal_request.json", &req)
	if req.Failure.TestName == "" || req.Culprit.Commit.SHA == "" || req.MaxAttempts != 3 {
		t.Errorf("heal request decoded with missing fields: %+v", req)
	}

	var ver VerifyResult
	decodeStrict(t, "verify_result.json", &ver)
	if ver.Status != "rejected" || len(ver.Guardrails) != 2 || ver.Guardrails[0].Passed {
		t.Errorf("verify result decoded wrongly: %+v", ver)
	}

	var run RunTestResult
	decodeStrict(t, "runtest_result.json", &run)
	if run.Verdict != "fail" || run.ExitCode != 1 {
		t.Errorf("runtest result decoded wrongly: %+v", run)
	}
}
