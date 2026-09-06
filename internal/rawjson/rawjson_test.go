package rawjson

import (
	"encoding/json"
	"strings"
	"testing"
)

// Servarr emits keys in its own order and callers read the output by eye. A
// decode-and-re-encode round trip would sort them; operating on the bytes does
// not.
func TestIndentPreservesKeyOrder(t *testing.T) {
	raw := json.RawMessage(`{"zulu":1,"alpha":2,"mike":3}`)
	got, err := Indent(raw)
	if err != nil {
		t.Fatalf("Indent = %v", err)
	}
	z, a, m := strings.Index(string(got), "zulu"), strings.Index(string(got), "alpha"), strings.Index(string(got), "mike")
	if !(z < a && a < m) {
		t.Fatalf("key order was not preserved:\n%s", got)
	}
}

func TestCompactPreserves2To53Plus1(t *testing.T) {
	raw := json.RawMessage("{\n  \"id\": 9007199254740993\n}")
	got, err := Compact(raw)
	if err != nil {
		t.Fatalf("Compact = %v", err)
	}
	if string(got) != `{"id":9007199254740993}` {
		t.Fatalf("Compact = %s, want the literal byte-exact", got)
	}
	back, err := Indent(got)
	if err != nil {
		t.Fatalf("Indent = %v", err)
	}
	if !strings.Contains(string(back), "9007199254740993") {
		t.Fatalf("the integer did not survive the round trip:\n%s", back)
	}
}

func TestSplitArrayOnNonArrayReportsFalse(t *testing.T) {
	for _, raw := range []string{`{"a":1}`, `"str"`, `42`, `null`, ``, `   `, `[1,2`} {
		if got, ok := SplitArray(json.RawMessage(raw)); ok {
			t.Errorf("SplitArray(%q) reported an array: %v", raw, got)
		}
	}
	els, ok := SplitArray(json.RawMessage(`[{"a":1},2,"three"]`))
	if !ok {
		t.Fatal("a top-level array was not split")
	}
	if len(els) != 3 || string(els[0]) != `{"a":1}` || string(els[2]) != `"three"` {
		t.Fatalf("elements = %v", els)
	}
	// An empty array is an array, with no elements.
	if els, ok = SplitArray(json.RawMessage(`[]`)); !ok || len(els) != 0 {
		t.Fatalf("empty array: %v %v", els, ok)
	}
}

// Top level only. A nested object in base is replaced wholesale by the
// overriding value, never merged into.
func TestMergeObjectsTopLevelOnly(t *testing.T) {
	base := json.RawMessage(`{"name":"old","nested":{"keep":1,"drop":2},"other":9007199254740993}`)
	over := map[string]json.RawMessage{
		"name":   json.RawMessage(`"new"`),
		"nested": json.RawMessage(`{"keep":3}`),
	}
	got, err := MergeObjects(base, over)
	if err != nil {
		t.Fatalf("MergeObjects = %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("result is not an object: %v (%s)", err, got)
	}
	if string(m["name"]) != `"new"` {
		t.Errorf("name = %s, want the override", m["name"])
	}
	if string(m["nested"]) != `{"keep":3}` {
		t.Errorf("nested = %s, want wholesale replacement with no deep merge", m["nested"])
	}
	if string(m["other"]) != "9007199254740993" {
		t.Errorf("other = %s, want the literal byte-exact", m["other"])
	}
}

func TestMergeObjectsRejectsNonObject(t *testing.T) {
	for _, raw := range []string{`[1,2]`, `"str"`, `42`, `null`, `{"a":`} {
		if _, err := MergeObjects(json.RawMessage(raw), map[string]json.RawMessage{"a": json.RawMessage(`1`)}); err == nil {
			t.Errorf("MergeObjects over %q was accepted", raw)
		}
	}
	// An empty base is an empty object, not an error.
	got, err := MergeObjects(nil, map[string]json.RawMessage{"a": json.RawMessage(`1`)})
	if err != nil {
		t.Fatalf("an empty base was rejected: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("got %s", got)
	}
}

func TestDecodeNumberUsesNumber(t *testing.T) {
	var v map[string]any
	if err := DecodeNumber(json.RawMessage(`{"id":9007199254740993}`), &v); err != nil {
		t.Fatalf("DecodeNumber = %v", err)
	}
	n, ok := v["id"].(json.Number)
	if !ok || n.String() != "9007199254740993" {
		t.Fatalf("id = %#v, want an exact json.Number", v["id"])
	}
	if err := DecodeNumber(json.RawMessage(`{"a":1} junk`), &v); err == nil {
		t.Fatal("trailing garbage was accepted")
	}
}
