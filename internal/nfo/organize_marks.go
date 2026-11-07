package nfo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"slices"
	"strings"
)

// ErrAutoMarksConflict means the NFO changed since preview; nothing was written.
var ErrAutoMarksConflict = errors.New("nfo: auto marks preview conflict")

// AutoMarksPlan describes only automatic tag organization. Digest identifies the
// original bytes read for this operation, including on a successful commit.
type AutoMarksPlan struct {
	Before    []string `json:"before"`
	After     []string `json:"after"`
	Owned     []string `json:"owned"`
	Digest    string   `json:"digest"`
	Protected bool     `json:"protected"`
	Changed   bool     `json:"changed"`
}

// PreviewAutoMarks reads an existing NFO under the shared path lock without writing.
func PreviewAutoMarks(path string, previous, current []string, overwrite bool) (AutoMarksPlan, error) {
	defer lockNFOPath(path)()
	plan, _, err := readAutoMarksPlan(path, previous, current, overwrite)
	return plan, err
}

// CommitAutoMarks compares the original digest and writes only changed tags,
// within the same shared path lock. Empty or stale digests never bypass comparison.
func CommitAutoMarks(path string, previous, current []string, overwrite bool, expectedDigest string) (AutoMarksPlan, error) {
	defer lockNFOPath(path)()
	plan, text, err := readAutoMarksPlan(path, previous, current, overwrite)
	if err != nil {
		return AutoMarksPlan{}, err
	}
	if plan.Digest != expectedDigest {
		return AutoMarksPlan{}, ErrAutoMarksConflict
	}
	if plan.Changed {
		if err := writeFileAtomic(path, text); err != nil {
			return AutoMarksPlan{}, err
		}
	}
	return plan, nil
}

// readAutoMarksPlan requires the caller to hold the existing NFO path lock.
func readAutoMarksPlan(path string, previous, current []string, overwrite bool) (AutoMarksPlan, string, error) {
	raw, err := ReadFile(path)
	if err != nil {
		return AutoMarksPlan{}, "", err
	}
	text := string(raw)
	if _, err := findClosingMovie(text); err != nil {
		return AutoMarksPlan{}, "", err
	}
	plan, err := mergeAutoMarkTags(text, previous, nil, current, overwrite, lockedTags(text, false)["tag"])
	if err != nil {
		return AutoMarksPlan{}, "", err
	}
	digest := sha256.Sum256(raw)
	plan.Digest = hex.EncodeToString(digest[:])
	// Equal logical tags are a no-op: preserve original tag formatting as well.
	if !plan.Protected && !slices.Equal(plan.Before, plan.After) {
		text = replaceBlocks(text, "tag", renderList("tag", plan.After, childIndent(text)), childIndent(text))
	}
	plan.Changed = text != string(raw)
	return plan, text, nil
}

// mergeAutoMarkTags is the shared D-2/A ownership policy for scraping and the
// narrow organizer. The caller validates the original NFO and holds its path lock.
func mergeAutoMarkTags(text string, previous, upstream, current []string, overwrite, locked bool) (AutoMarksPlan, error) {
	plan := AutoMarksPlan{Before: []string{}, After: []string{}, Owned: []string{}}
	for _, element := range elementsNamed(text, "tag") {
		var value string
		if err := xml.Unmarshal([]byte(text[element.start:element.end]), &value); err != nil {
			return AutoMarksPlan{}, err
		}
		plan.Before = append(plan.Before, strings.TrimSpace(value))
	}
	previous = trimAll(previous)
	plan.Protected = locked || (!overwrite && len(plan.Before) > 0)
	if plan.Protected {
		plan.After = slices.Clone(plan.Before)
		for _, value := range previous {
			if slices.Contains(plan.Before, value) && !slices.Contains(plan.Owned, value) {
				plan.Owned = append(plan.Owned, value)
			}
		}
		return plan, nil
	}
	for _, value := range plan.Before {
		if !slices.Contains(previous, value) {
			plan.After = append(plan.After, value)
		}
	}
	for _, value := range trimAll(upstream) {
		if !slices.Contains(plan.After, value) {
			plan.After = append(plan.After, value)
		}
	}
	for _, value := range trimAll(current) {
		if slices.Contains(plan.After, value) {
			continue
		}
		plan.After = append(plan.After, value)
		plan.Owned = append(plan.Owned, value)
	}
	return plan, nil
}
