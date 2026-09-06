// Package rawjson works on JSON bytes rather than on decoded values, so key
// order and number literals survive. Servarr and Trakt both return integers
// wider than a float64 can hold, and a decode-and-re-encode round trip
// silently rewrites them; every helper here avoids that round trip.
//
// It imports the standard library only, because it is copied byte-for-byte
// into traktctl. (plexctl keeps map[string]any with UseNumber and does not
// take this package.)
package rawjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Indent pretty-prints raw without decoding it.
func Indent(raw json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Compact strips insignificant whitespace from raw without decoding it.
func Compact(raw json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SplitArray returns the elements of a top-level array and true. Anything else
// — an object, a scalar, invalid or empty input — returns nil and false, so the
// caller can fall back rather than having to distinguish an error from a
// not-an-array.
func SplitArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	// A JSON null unmarshals into a slice without error, so the opening
	// bracket is checked rather than inferred from a successful decode.
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var els []json.RawMessage
	if err := json.Unmarshal(raw, &els); err != nil {
		return nil, false
	}
	return els, true
}

// MergeObjects overlays over onto base's top-level keys. There is no deep
// merge: a key present in over replaces base's value wholesale, nested object
// and all. base must be a JSON object; an empty base counts as an empty one.
//
// Keys that base already had keep their original position, and keys that only
// over supplies are appended in sorted order, so the same inputs always produce
// the same bytes. Values are copied verbatim from whichever side supplied them.
func MergeObjects(base json.RawMessage, over map[string]json.RawMessage) (json.RawMessage, error) {
	order, existing, err := objectKeys(base)
	if err != nil {
		return nil, err
	}

	added := make([]string, 0, len(over))
	for k := range over {
		if _, had := existing[k]; !had {
			added = append(added, k)
		}
	}
	sort.Strings(added)

	var buf bytes.Buffer
	buf.WriteByte('{')
	write := func(k string, v json.RawMessage) error {
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return err
		}
		buf.Write(key)
		buf.WriteByte(':')
		compact, err := Compact(v)
		if err != nil {
			return fmt.Errorf("value for key %q is not valid JSON: %w", k, err)
		}
		buf.Write(compact)
		return nil
	}
	for _, k := range order {
		v := existing[k]
		if o, ok := over[k]; ok {
			v = o
		}
		if err := write(k, v); err != nil {
			return nil, err
		}
	}
	for _, k := range added {
		if err := write(k, over[k]); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// objectKeys returns base's top-level keys in the order they appear, with their
// raw values. An empty base is an empty object.
func objectKeys(base json.RawMessage) ([]string, map[string]json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(base)) == 0 {
		return nil, values, nil
	}
	dec := json.NewDecoder(bytes.NewReader(base))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, errors.New("expected a JSON object")
	}
	var order []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		k, ok := kt.(string)
		if !ok {
			return nil, nil, errors.New("expected a JSON object key")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if _, dup := values[k]; !dup {
			order = append(order, k)
		}
		values[k] = v // last duplicate wins, as encoding/json does
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, nil, err
	}
	return order, values, nil
}

// DecodeNumber decodes raw into into with UseNumber set and requires that raw
// holds exactly one JSON value, matching the strict decode used on the wire.
// An empty or whitespace-only raw leaves into untouched and is not an error.
//
// The logic is duplicated from xhttp.DecodeOne rather than imported: these
// packages copy into repositories that take one and not the other, so rawjson
// depends on the standard library alone.
func DecodeNumber(raw json.RawMessage, into any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(into); err != nil {
		return err
	}
	var rest json.RawMessage
	if err := dec.Decode(&rest); err != io.EOF {
		if err != nil {
			return fmt.Errorf("unexpected content after the JSON value: %w", err)
		}
		return errors.New("unexpected second JSON value after the first")
	}
	return nil
}
