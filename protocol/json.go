package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ValidateJSON rejects ambiguous duplicate members and excessively deep input
// before ProtoJSON decoding. JSON implementations otherwise disagree on these.
func ValidateJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("JSON exceeds maximum depth 64")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		opening, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch opening {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				token, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || keys[key] {
					return fmt.Errorf("duplicate or invalid JSON member %q", token)
				}
				keys[key] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}
