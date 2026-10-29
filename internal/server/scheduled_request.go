package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// A schedule is small configuration, not a bulk input. Bound all bodies while
// retaining legacy parameter semantics for older task types. New sprite tasks
// additionally reject ambiguous field spelling, duplicate keys and null values.
func decodeScheduledRequest(r io.Reader) (scheduledRequest, error) {
	var req scheduledRequest
	data, err := io.ReadAll(io.LimitReader(r, 65537))
	if err != nil || len(data) > 65536 {
		return req, errors.New("invalid or oversized scheduled request")
	}
	if err = json.Unmarshal(data, &req); err != nil {
		return req, errors.New("invalid scheduled JSON")
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), "sprites") {
		_, raw, e := artworkObject(bytes.NewReader(data))
		if e != nil {
			return req, errors.New("ambiguous sprites schedule")
		}
		for key := range raw {
			switch key {
			case "name", "type", "cron", "enabled", "params":
			default:
				return req, errors.New("invalid sprites schedule fields")
			}
		}
		if params, ok := raw["params"]; ok {
			if _, _, e = artworkObject(bytes.NewReader(params)); e != nil {
				return req, errors.New("ambiguous sprites parameters")
			}
		}
	}
	return req, nil
}
