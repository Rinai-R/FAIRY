package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

func writeOutput(writer io.Writer, format string, value any) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(value)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		_, err = fmt.Fprintln(writer, string(raw))
		return err
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := bytes.TrimSpace(object[key])
		if _, err := fmt.Fprintf(writer, "%s\t%s\n", key, value); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONLine(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
