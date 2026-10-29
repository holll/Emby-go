package artwork

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image/jpeg"
	"io"
	"math"
	"os"
)

// Roku BIF v0: https://developer.roku.com/docs/developer-program/media-playback/trick-mode/bif-file-creation.md
// Little-endian header (64 bytes), N+1 (timestamp, absolute offset) entries,
// contiguous JPEGs, final timestamp 0xffffffff and EOF offset. Multiplier=1ms.
var spriteBIFMagic = []byte{0x89, 0x42, 0x49, 0x46, 0x0d, 0x0a, 0x1a, 0x0a}

type spriteBIF struct {
	file        *os.File
	count, done int
	size        int64
	hash        string
}

func newSpriteBIF(root *os.Root, path string, count int) (*spriteBIF, error) {
	if count < 1 || count > spriteMaxFrames {
		return nil, errors.New("invalid BIF frame count")
	}
	if err := safe(root, path); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 64+(count+1)*8)
	copy(header, spriteBIFMagic)
	binary.LittleEndian.PutUint32(header[12:16], uint32(count))
	binary.LittleEndian.PutUint32(header[16:20], 1)
	if _, err = f.Write(header); err != nil {
		f.Close()
		return nil, err
	}
	return &spriteBIF{file: f, count: count, size: int64(len(header))}, nil
}
func (b *spriteBIF) frame(timestamp uint32, jpg []byte) error {
	if b.done >= b.count || timestamp == math.MaxUint32 || len(jpg) == 0 || len(jpg) > spriteMaxFrameBytes || b.size+int64(len(jpg)) > spriteMaxBytes || b.size+int64(len(jpg)) >= math.MaxUint32 {
		return errors.New("BIF exceeds frame or byte limit")
	}
	var entry [8]byte
	binary.LittleEndian.PutUint32(entry[:4], timestamp)
	binary.LittleEndian.PutUint32(entry[4:], uint32(b.size))
	if _, err := b.file.WriteAt(entry[:], 64+int64(b.done)*8); err != nil {
		return err
	}
	if _, err := b.file.Write(jpg); err != nil {
		return err
	}
	b.size += int64(len(jpg))
	b.done++
	return nil
}
func (b *spriteBIF) finish() error {
	if b.done != b.count {
		return errors.New("incomplete BIF")
	}
	var entry [8]byte
	binary.LittleEndian.PutUint32(entry[:4], math.MaxUint32)
	binary.LittleEndian.PutUint32(entry[4:], uint32(b.size))
	if _, err := b.file.WriteAt(entry[:], 64+int64(b.count)*8); err != nil {
		return err
	}
	if err := b.file.Sync(); err != nil {
		return err
	}
	if _, err := b.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(b.file, spriteMaxBytes+1))
	if err != nil {
		return err
	}
	if n != b.size {
		return errors.New("invalid BIF size")
	}
	b.hash = hex.EncodeToString(h.Sum(nil))
	return b.close()
}
func (b *spriteBIF) close() error {
	if b.file == nil {
		return nil
	}
	err := b.file.Close()
	b.file = nil
	return err
}
func validateSpriteBIF(data []byte, m SpriteManifest) error {
	headerLen := 64 + (m.Count+1)*8
	if m.Count < 1 || m.Count > spriteMaxFrames || len(data) < headerLen || len(data) > spriteMaxBytes || !bytes.Equal(data[:8], spriteBIFMagic) || binary.LittleEndian.Uint32(data[8:12]) != 0 || binary.LittleEndian.Uint32(data[12:16]) != uint32(m.Count) || binary.LittleEndian.Uint32(data[16:20]) != 1 {
		return errors.New("invalid BIF header")
	}
	for _, v := range data[20:64] {
		if v != 0 {
			return errors.New("invalid BIF reserved bytes")
		}
	}
	previous := uint32(headerLen)
	for i := 0; i < m.Count; i++ {
		entry := data[64+i*8 : 72+i*8]
		timestamp := binary.LittleEndian.Uint32(entry[:4])
		offset := binary.LittleEndian.Uint32(entry[4:])
		next := binary.LittleEndian.Uint32(data[76+i*8 : 80+i*8])
		if int64(timestamp) != int64(i)*m.IntervalMs || offset != previous || next <= offset || uint64(next) > uint64(len(data)) || next-offset > spriteMaxFrameBytes {
			return errors.New("invalid BIF index")
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data[offset:next]))
		if err != nil || cfg.Width != spriteWidth || cfg.Height != spriteHeight {
			return errors.New("invalid BIF JPEG")
		}
		previous = next
	}
	terminal := data[64+m.Count*8 : 72+m.Count*8]
	if binary.LittleEndian.Uint32(terminal[:4]) != math.MaxUint32 || binary.LittleEndian.Uint32(terminal[4:]) != uint32(len(data)) || previous != uint32(len(data)) {
		return errors.New("invalid BIF terminator")
	}
	return nil
}
