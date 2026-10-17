package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// decodeEditPatch is the new strict adapter only. PUT retains legacy null/empty
// semantics. Pointers preserve omission; explicit null is not a clearing value.
func decodeEditPatch(body io.Reader) (editRequest, error) {
	var req editRequest
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&raw); err != nil {
		return req, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return req, fmt.Errorf("one JSON object required")
	}
	if len(raw) == 0 {
		return req, fmt.Errorf("metadata fields required")
	}
	for key, value := range raw {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return req, fmt.Errorf("null is not supported")
		}
		switch key {
		case "genres", "tags", "studios", "locked_fields":
			var items []json.RawMessage
			if err := json.Unmarshal(value, &items); err != nil {
				return req, err
			}
			for _, item := range items {
				if bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
					return req, fmt.Errorf("null list item is not supported")
				}
			}
		case "title", "number", "original_title", "plot", "year", "maker", "label", "lock_data":
			// Exact canonical names only: encoding/json's case-insensitive struct
			// matching must not bypass the list validation above.
		default:
			return req, fmt.Errorf("unknown metadata field")
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return req, err
	}
	strict := json.NewDecoder(bytes.NewReader(encoded))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&req); err != nil {
		return req, err
	}
	if req.Year != nil && *req.Year < 0 {
		return req, fmt.Errorf("year must be nonnegative")
	}
	return req, nil
}

// clearTags carries only explicit empty values from PATCH to the NFO writer.
// All other pointers, including false lock_data, use the normal update path.
func (r editRequest) clearTags() []string {
	var tags []string
	for _, field := range []struct {
		tag   string
		value *string
	}{
		{"title", r.Title}, {"num", r.Number}, {"originaltitle", r.OriginalTitle},
		{"plot", r.Plot}, {"maker", r.Maker}, {"label", r.Label},
	} {
		if field.value != nil && strings.TrimSpace(*field.value) == "" {
			tags = append(tags, field.tag)
		}
	}
	for _, field := range []struct {
		tag   string
		value *[]string
	}{
		{"genre", r.Genres}, {"tag", r.Tags}, {"studio", r.Studios},
	} {
		if field.value != nil && len(trimList(*field.value)) == 0 {
			tags = append(tags, field.tag)
		}
	}
	if r.Year != nil && *r.Year == 0 {
		tags = append(tags, "year")
	}
	return tags
}
