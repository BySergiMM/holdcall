package clientconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// orderedObject is a JSON object decoded with its key order kept and every
// value held as raw, unparsed bytes.
//
// encoding/json's own way of decoding an object into a map loses the order --
// Go maps have none, and re-encoding a map sorts keys alphabetically -- which
// would reorder an operator's whole config file for a change to one entry.
// This keeps the order it was read in, and keeps every value nim init did
// not touch as the exact bytes it read, so rewriting one MCP server does not
// reformat or reshuffle the rest of the file.
type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

// decodeOrderedObject requires raw to be a JSON object; anything else -- an
// array, a scalar, or invalid JSON -- is refused, mirroring nim init's rule
// that a client config file must itself be a JSON object.
func decodeOrderedObject(raw json.RawMessage) (orderedObject, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return orderedObject{}, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return orderedObject{}, fmt.Errorf("expected a JSON object, got %v", tok)
	}

	o := orderedObject{values: map[string]json.RawMessage{}}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return orderedObject{}, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return orderedObject{}, fmt.Errorf("expected a string key, got %v", keyTok)
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return orderedObject{}, fmt.Errorf("decoding value for %q: %w", key, err)
		}
		// A duplicate key keeps its first position but its last value, the
		// same as encoding/json's own map decoding would leave it. Real
		// files never do this; it is handled rather than assumed away.
		if _, exists := o.values[key]; !exists {
			o.keys = append(o.keys, key)
		}
		o.values[key] = val
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return orderedObject{}, err
	}
	return o, nil
}

// set adds or replaces a key, appending it at the end when it is new so an
// entry gains "command"/"args" in a stable position rather than wherever a
// map iteration happens to put them.
func (o orderedObject) set(key string, val json.RawMessage) orderedObject {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = val
	return o
}

// marshalIndent renders the object with two-space indentation, in the key
// order it was built with. Untouched values are re-indented to match rather
// than kept byte-identical, since they may have come from a differently
// indented file; their content and position are what is preserved, not their
// original whitespace.
func (o orderedObject) marshalIndent() (json.RawMessage, error) {
	if len(o.keys) == 0 {
		return json.RawMessage("{}"), nil
	}
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range o.keys {
		keyBytes, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		var val bytes.Buffer
		if err := json.Indent(&val, o.values[k], "  ", "  "); err != nil {
			return nil, fmt.Errorf("re-indenting %q: %w", k, err)
		}
		buf.WriteString("  ")
		buf.Write(keyBytes)
		buf.WriteString(": ")
		buf.Write(val.Bytes())
		if i < len(o.keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}")
	return buf.Bytes(), nil
}

// prettyOrRaw renders raw indented for display in a diff. Falling back to
// the raw bytes rather than erroring keeps a diff readable even for a value
// this package could not fully make sense of.
func prettyOrRaw(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

func rawString(s string) json.RawMessage {
	b, _ := json.Marshal(s) // a Go string always marshals
	return b
}

func rawStrings(s []string) json.RawMessage {
	b, _ := json.Marshal(s) // a []string always marshals
	return b
}
