package main

import (
	"errors"
	"strconv"
	"strings"
)

// textRecord decodes one slog text line into its keys and values, all
// strings. It fails on anything that is not key=value pairs, such as JSON.
func textRecord(line string) (map[string]any, error) {
	record := map[string]any{}
	for line = strings.TrimSpace(line); line != ""; line = strings.TrimLeft(line, " ") {
		key, rest, ok := strings.Cut(line, "=")
		if !ok || key == "" || strings.ContainsAny(key, " \"{}") {
			return nil, errors.New("not a text record")
		}
		var value string
		if strings.HasPrefix(rest, `"`) {
			quoted, err := strconv.QuotedPrefix(rest)
			if err != nil {
				return nil, err
			}
			value, _ = strconv.Unquote(quoted)
			rest = rest[len(quoted):]
		} else {
			value, rest, _ = strings.Cut(rest, " ")
			rest = " " + rest
		}
		if rest != "" && rest[0] != ' ' {
			return nil, errors.New("not a text record")
		}
		record[key] = value
		line = rest
	}
	if len(record) == 0 {
		return nil, errors.New("empty record")
	}
	return record, nil
}
