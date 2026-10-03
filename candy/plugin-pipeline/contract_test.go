package pluginpipeline

import (
	"testing"
)

// TestTypedDecode_ContractViolations: the typed decoder rejects out-of-contract
// replies with the exact field + expected type — the informed redo signal.
func TestTypedDecode_ContractViolations(t *testing.T) {
	// a valid reply decodes
	v, err := decodeTypedOutput(`{"class": "visual", "tier": "vm", "checks": [{"id": "a", "assertion": "grep -q x /f"}]}`, "class", map[string]any{"type": "enum", "enum": []any{"system", "packaging", "visual", "skip"}})
	if err != nil || v != "visual" {
		t.Fatalf("valid enum decode: v=%v err=%v", v, err)
	}
	// an out-of-enum value is rejected with the allowed list
	_, err = decodeTypedOutput(`{"class": "bogus"}`, "class", map[string]any{"type": "enum", "enum": []any{"system", "packaging", "visual"}})
	if err == nil {
		t.Fatal("out-of-enum value must be rejected")
	}
	// a missing field is rejected with the contract
	_, err = decodeTypedOutput(`{"other": 1}`, "class", map[string]any{"type": "string"})
	if err == nil {
		t.Fatal("a missing declared output must be rejected")
	}
	// a string_list validates
	v, err = decodeTypedOutput(`{"files": ["a", "b"]}`, "files", map[string]any{"type": "string_list"})
	if err != nil || len(v.([]string)) != 2 {
		t.Fatalf("string_list decode: v=%v err=%v", v, err)
	}
	// a non-JSON reply is rejected informatively
	_, err = decodeTypedOutput("just prose, no object", "class", map[string]any{"type": "string"})
	if err == nil {
		t.Fatal("a prose-only reply must be rejected")
	}
}
