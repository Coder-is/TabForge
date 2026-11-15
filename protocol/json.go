package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// ValidateJSON rejects ambiguous duplicate members and excessively deep input
// before ProtoJSON decoding. JSON implementations otherwise disagree on these.
func ValidateJSON(data []byte) error {
	if json.Valid(data) {
		c := jsonChecker{data: data}
		if c.value(0) {
			return nil
		}
	}
	// Rejected: the token walker reports why, with the messages it always had.
	if err := validateJSONSlow(data); err != nil {
		return err
	}
	return fmt.Errorf("invalid JSON")
}

func validateJSONSlow(data []byte) error {
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

// jsonChecker walks input json.Valid accepted, checking depth and duplicate
// members like validateJSONSlow without a token per value. Member names are
// compared after unescaping, as Decoder.Token returns them.
type jsonChecker struct {
	data []byte
	i    int
	keys [][]byte // names of the objects being walked, innermost last
}

func (c *jsonChecker) space() {
	for c.i < len(c.data) {
		switch c.data[c.i] {
		case ' ', '\t', '\n', '\r':
			c.i++
		default:
			return
		}
	}
}

// str skips a string at c.i and returns its raw contents.
func (c *jsonChecker) str() []byte {
	c.i++
	start := c.i
	for c.data[c.i] != '"' {
		if c.data[c.i] == '\\' {
			c.i++
		}
		c.i++
	}
	raw := c.data[start:c.i]
	c.i++
	return raw
}

func (c *jsonChecker) key() []byte {
	raw := c.str()
	if bytes.IndexByte(raw, '\\') < 0 && utf8.Valid(raw) {
		return raw
	}
	var s string
	json.Unmarshal(c.data[c.i-len(raw)-2:c.i], &s) // Valid input: cannot fail.
	return []byte(s)
}

func (c *jsonChecker) value(depth int) bool {
	if depth > 64 {
		return false
	}
	c.space()
	switch c.data[c.i] {
	case '{':
		c.i++
		c.space()
		if c.data[c.i] == '}' {
			c.i++
			return true
		}
		mark := len(c.keys)
		var seen map[string]bool // for large objects
		for {
			c.space()
			key := c.key()
			if seen != nil {
				if seen[string(key)] {
					return false
				}
				seen[string(key)] = true
			} else {
				for _, other := range c.keys[mark:] {
					if bytes.Equal(other, key) {
						return false
					}
				}
				c.keys = append(c.keys, key)
				if len(c.keys)-mark > 16 {
					seen = make(map[string]bool, 32)
					for _, k := range c.keys[mark:] {
						seen[string(k)] = true
					}
				}
			}
			c.space()
			c.i++ // ':'
			if !c.value(depth + 1) {
				return false
			}
			c.space()
			if c.data[c.i] == ',' {
				c.i++
				continue
			}
			c.i++ // '}'
			c.keys = c.keys[:mark]
			return true
		}
	case '[':
		c.i++
		c.space()
		if c.data[c.i] == ']' {
			c.i++
			return true
		}
		for {
			if !c.value(depth + 1) {
				return false
			}
			c.space()
			if c.data[c.i] == ',' {
				c.i++
				continue
			}
			c.i++ // ']'
			return true
		}
	case '"':
		c.str()
	default: // number, true, false or null
		for c.i < len(c.data) {
			switch c.data[c.i] {
			case ',', ']', '}', ' ', '\t', '\n', '\r':
				return true
			}
			c.i++
		}
	}
	return true
}
