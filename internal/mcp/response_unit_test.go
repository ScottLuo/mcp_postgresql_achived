//go:build unit

package mcp

import (
	"encoding/json"
	"testing"
)

// TestToolResult_JSONRoundTrip_Success verifies that a successful ToolResult
// marshals to the spec §10.4 JSON shape and round-trips cleanly.
func TestToolResult_JSONRoundTrip_Success(t *testing.T) {
	in := &ToolResult{
		Success: true,
		Data:    map[string]interface{}{"id": 1, "name": "orders"},
		Metadata: &Metadata{
			Target:       "development",
			DurationMS:   12,
			Truncated:    false,
			RowsReturned: 1,
		},
	}

	b, err := Serialize(in)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	// Verify the JSON has the expected top-level keys.
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if m["success"] != true {
		t.Errorf("success = %v, want true", m["success"])
	}
	if _, ok := m["error"]; ok {
		t.Error("success result must not include 'error' field")
	}
	meta, ok := m["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("metadata missing or not an object")
	}
	if meta["duration_ms"] != float64(12) {
		t.Errorf("duration_ms = %v, want 12", meta["duration_ms"])
	}
	if meta["rows_returned"] != float64(1) {
		t.Errorf("rows_returned = %v, want 1", meta["rows_returned"])
	}

	// Round-trip back into a ToolResult.
	var out ToolResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("round-trip Unmarshal failed: %v", err)
	}
	if !out.Success {
		t.Error("round-tripped Success = false")
	}
	if out.Metadata == nil || out.Metadata.DurationMS != 12 {
		t.Errorf("round-tripped Metadata wrong: %+v", out.Metadata)
	}
}

// TestToolResult_JSONRoundTrip_Error verifies each of the 11 standard error
// codes serializes with the correct "error.code" value and no "data".
func TestToolResult_JSONRoundTrip_Error(t *testing.T) {
	for _, code := range AllErrorCodes {
		t.Run(code, func(t *testing.T) {
			in := ErrorResult(code, "something went wrong", &Metadata{
				Target:       "production",
				DurationMS:   3,
				RowsReturned: 0,
			})

			b, err := Serialize(in)
			if err != nil {
				t.Fatalf("Serialize failed: %v", err)
			}

			var m map[string]interface{}
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if m["success"] != false {
				t.Errorf("success = %v, want false", m["success"])
			}
			errObj, ok := m["error"].(map[string]interface{})
			if !ok {
				t.Fatal("error object missing")
			}
			if errObj["code"] != code {
				t.Errorf("error.code = %v, want %q", errObj["code"], code)
			}
			if errObj["message"] != "something went wrong" {
				t.Errorf("error.message = %v", errObj["message"])
			}
		})
	}
}

// TestToolResult_AllErrorCodesUnique verifies all 11 error-code constants are
// distinct strings and there are exactly 11 of them.
func TestToolResult_AllErrorCodesUnique(t *testing.T) {
	if len(AllErrorCodes) != 11 {
		t.Errorf("len(AllErrorCodes) = %d, want 11", len(AllErrorCodes))
	}
	seen := make(map[string]bool)
	for _, c := range AllErrorCodes {
		if seen[c] {
			t.Errorf("duplicate error code %q", c)
		}
		seen[c] = true
	}
}

// TestToolResult_ErrorResult_WithMetadata verifies metadata fields serialize
// correctly on an error result.
func TestToolResult_ErrorResult_WithMetadata(t *testing.T) {
	in := ErrorResult(ErrCodeQueryTimeout, "timeout", &Metadata{
		Target:       "dev",
		DurationMS:   10000,
		Truncated:    true,
		RowsReturned: 500,
	})
	b, err := Serialize(in)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	meta, _ := m["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatal("metadata missing")
	}
	if meta["truncated"] != true {
		t.Error("truncated = false, want true")
	}
	if meta["rows_returned"] != float64(500) {
		t.Errorf("rows_returned = %v", meta["rows_returned"])
	}
}

// TestSerialize_Nil verifies serializing a nil result returns an error.
func TestSerialize_Nil(t *testing.T) {
	_, err := Serialize(nil)
	if err == nil {
		t.Error("expected error serializing nil ToolResult")
	}
}

// TestSuccessResult_Data verifies data is preserved on a success result.
func TestSuccessResult_Data(t *testing.T) {
	data := []string{"a", "b", "c"}
	res := SuccessResult(data, nil)
	if !res.Success {
		t.Error("Success = false")
	}
	if res.Error != nil {
		t.Error("Error should be nil on success")
	}
	if len(res.Data.([]string)) != 3 {
		t.Errorf("Data length = %d, want 3", len(res.Data.([]string)))
	}
}

// TestNewToolError verifies the constructor.
func TestNewToolError(t *testing.T) {
	e := NewToolError(ErrCodeSQLInvalid, "bad sql")
	if e.Code != ErrCodeSQLInvalid {
		t.Errorf("Code = %q", e.Code)
	}
	if e.Message != "bad sql" {
		t.Errorf("Message = %q", e.Message)
	}
}
