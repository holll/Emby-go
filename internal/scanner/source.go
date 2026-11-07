package scanner

import (
	"errors"
	"io"
	"strings"

	"emby-go/internal/regularfile"
)

const MaxSourceSize = 1 << 20

var ErrSourceTooLarge = errors.New("STRM exceeds 1 MiB limit")

// readSourceFile bounds the entire file, not just its first URL line.
// Both ordinary reads and incremental fingerprints use this boundary.
func readSourceFile(path string) ([]byte, error) {
	file, err := regularfile.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxSourceSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSourceSize {
		return nil, ErrSourceTooLarge
	}
	return data, nil
}

func ReadSource(path string) (string, error) {
	data, err := readSourceFile(path)
	if err != nil {
		return "", err
	}
	text := string(data)
	line := firstLine(text)
	if line == "" && !strings.Contains(text, "\n") {
		return "", io.EOF
	}
	return line, nil
}
