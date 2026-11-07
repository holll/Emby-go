package probe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These are real-ffprobe candidate-mechanism tests, not coverage of Run's
// current URL execution path. Opt in explicitly; a missing binary is a skip,
// never evidence that production network isolation has been implemented.
func TestFFprobePipeInputBoundary(t *testing.T) {
	path := os.Getenv("EMBY_GO_TEST_FFPROBE")
	if path == "" {
		t.Skip("set EMBY_GO_TEST_FFPROBE to exercise a real ffprobe binary")
	}
	binaryPath, err := exec.LookPath(path)
	if err != nil {
		t.Fatal(err)
	}
	// A finite, self-contained PCM file; no ffmpeg fixture generator is needed.
	wav := make([]byte, 44+16000)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 8000)
	binary.LittleEndian.PutUint32(wav[28:], 16000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 16000)

	var hits atomic.Int64
	protected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/redirect.wav" {
			http.Redirect(w, r, "/media.wav", http.StatusFound)
			return
		}
		_, _ = w.Write(wav)
	}))
	defer protected.Close()

	const formats = "mov,matroska,webm,mpegts,avi,flv,wav,flac,mp3,ogg,mpeg"
	run := func(t *testing.T, input []byte, restrictFormats, cached bool) ([]byte, []byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		inputURL, protocols := "pipe:0", "pipe"
		if cached {
			inputURL, protocols = "cache:pipe:0", "cache,pipe"
		}
		args := []string{"-v", "error", "-protocol_whitelist", protocols}
		if cached {
			// This controls seek-ahead only, NOT a total input or disk quota.
			args = append(args, "-read_ahead_limit", "67108864")
		}
		if bytes.HasPrefix(input, []byte("ffconcat version 1.0")) {
			// Exercise the protocol boundary, not concat's separate safe-path check.
			args = append(args, "-safe", "0")
		}
		if restrictFormats {
			args = append(args, "-format_whitelist", formats)
		}
		args = append(args, "-print_format", "json", "-show_format", "-show_streams", inputURL)
		cmd := exec.CommandContext(ctx, binaryPath, args...)
		configureProcess(cmd)
		cmd.Stdin = bytes.NewReader(input)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.WaitDelay = time.Second
		err := cmd.Run()
		if cmd.Process != nil {
			_ = killProcess(cmd)
		}
		if ctx.Err() != nil {
			t.Fatalf("finite input did not terminate within candidate-test budget: %v", ctx.Err())
		}
		return stdout.Bytes(), stderr.Bytes(), err
	}

	for _, cached := range []bool{false, true} {
		mode := "pipe"
		if cached {
			mode = "cache-pipe"
		}
		t.Run(mode, func(t *testing.T) {
			t.Run("self-contained-wav", func(t *testing.T) {
				stdout, stderr, err := run(t, wav, true, cached)
				if err != nil {
					t.Fatalf("WAV failed: %v: %s", err, stderr)
				}
				var output ffprobeOutput
				if err := json.Unmarshal(stdout, &output); err != nil {
					t.Fatal(err)
				}
				if len(output.Streams) != 1 || output.Streams[0].CodecName != "pcm_s16le" || output.Streams[0].SampleRate.Int() != 8000 {
					t.Fatalf("unexpected WAV stream: %s", stdout)
				}
				if cached && (output.Format.Duration.Int64() != 1 || output.Format.Size.Int64() != int64(len(wav))) {
					t.Fatalf("finite cached WAV metadata incomplete: %s", stdout)
				}
				// Do not equate a successful pipe probe with seekable-input parity.
				var raw struct {
					Format struct {
						Filename string `json:"filename"`
					} `json:"format"`
				}
				expectedName := "pipe:0"
				if cached {
					expectedName = "cache:pipe:0"
				}
				if err := json.Unmarshal(stdout, &raw); err != nil || raw.Format.Filename != expectedName {
					t.Fatalf("input identity escaped pipe boundary: %s, %v", stdout, err)
				}
			})

			inputs := []struct{ name, text string }{
				{"hls-http", "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n" + protected.URL + "/media.wav\n#EXT-X-ENDLIST\n"},
				{"hls-redirect", "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n" + protected.URL + "/redirect.wav\n#EXT-X-ENDLIST\n"},
				{"hls-key", "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-KEY:METHOD=AES-128,URI=\"" + protected.URL + "/key\"\n#EXTINF:1,\n" + protected.URL + "/media.wav\n#EXT-X-ENDLIST\n"},
				{"concat-http", "ffconcat version 1.0\nfile '" + protected.URL + "/media.wav'\n"},
			}
			for _, input := range inputs {
				for _, restrict := range []bool{false, true} {
					name := input.name + "/pipe-only"
					if restrict {
						name = input.name + "/pipe-and-formats"
					}
					t.Run(name, func(t *testing.T) {
						before := hits.Load()
						_, stderr, err := run(t, []byte(input.text), restrict, cached)
						if err == nil {
							t.Fatalf("external-resource input unexpectedly succeeded: %s", stderr)
						}
						if got := hits.Load() - before; got != 0 {
							t.Fatalf("protected listener received %d nested requests", got)
						}
						if !strings.Contains(string(stderr), "whitelist") || (!restrict && !strings.Contains(string(stderr), "Protocol 'http'")) {
							t.Fatalf("protocol/format restriction was not evidenced: %s", stderr)
						}
					})
				}
			}
		})
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("protected listener hits=%d", got)
	}
}
