package artwork

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The caller owns root and holds the movie's sprite lock through finish.
// Backing is immutable; canonical is a separate copy so external edits to the
// visible sidecar cannot change the generation used by authenticated reads.
type spriteBIFAlias struct {
	root       *os.Root
	canonical  string
	staged     string
	backup     string
	stagedInfo os.FileInfo
	swapped    bool
}

func prepareSpriteBIFAlias(root *os.Root, backing, canonical string) (alias *spriteBIFAlias, err error) {
	if !filepath.IsLocal(backing) || !filepath.IsLocal(canonical) || filepath.Clean(backing) != backing || filepath.Clean(canonical) != canonical || backing == canonical || filepath.Dir(backing) != filepath.Dir(canonical) {
		return nil, errors.New("BIF alias paths must be distinct local files in the same directory")
	}
	if err := safe(root, canonical); err != nil {
		return nil, err
	}
	a := &spriteBIFAlias{root: root, canonical: canonical}
	defer func() {
		if err != nil {
			err = errors.Join(err, a.finish(false))
		}
	}()
	// Both large copies and fsyncs happen before the store write transaction.
	copyTemp := func(source string) (name string, err error) {
		in, err := openRegular(root, source)
		if err != nil {
			return "", err
		}
		defer in.Close()
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name = filepath.Join(filepath.Dir(canonical), ".bif-alias-"+hex.EncodeToString(random[:])+".tmp")
		out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "", err
		}
		defer func() {
			err = errors.Join(err, out.Close())
			if err != nil {
				err = errors.Join(err, root.Remove(name))
			}
		}()
		n, err := io.Copy(out, io.LimitReader(in, spriteMaxBytes+1))
		if err != nil {
			return name, err
		}
		if n > spriteMaxBytes {
			return name, errors.New("BIF alias exceeds byte limit")
		}
		return name, out.Sync()
	}
	if a.staged, err = copyTemp(backing); err != nil {
		// copyTemp owns cleanup of a failed copy.
		a.staged = ""
		return nil, err
	}
	if a.stagedInfo, err = root.Lstat(a.staged); err != nil {
		return nil, err
	}
	if _, err = root.Lstat(canonical); err == nil {
		if a.backup, err = copyTemp(canonical); err != nil {
			a.backup = ""
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return a, nil
}

// commit performs only the constant-time publication inside the DB transaction.
func (a *spriteBIFAlias) commit() error {
	if err := a.root.Rename(a.staged, a.canonical); err != nil {
		return err
	}
	a.staged = ""
	a.swapped = true
	return nil
}

// A failed restore retains the backup and returns its error, rather than
// deleting the only remaining copy of the original canonical sidecar.
func (a *spriteBIFAlias) finish(committed bool) error {
	if a.swapped && !committed {
		info, err := a.root.Lstat(a.canonical)
		if err != nil {
			return err
		}
		if !os.SameFile(info, a.stagedInfo) {
			return errors.New("BIF alias changed before rollback")
		}
		if a.backup != "" {
			if err := a.root.Rename(a.backup, a.canonical); err != nil {
				return fmt.Errorf("restore BIF alias backup %s: %w", a.backup, err)
			}
			a.backup = ""
		} else if err := a.root.Remove(a.canonical); err != nil {
			return err
		}
		a.swapped = false
	}
	if committed {
		a.swapped = false // A later cleanup cannot roll back a committed alias.
	}
	var result error
	for _, path := range []*string{&a.staged, &a.backup} {
		if *path != "" {
			if err := a.root.Remove(*path); err != nil {
				result = errors.Join(result, err)
			} else {
				*path = ""
			}
		}
	}
	// Directory durability belongs outside the DB transaction. A sync failure
	// after publication is reported without revoking the committed canonical.
	return errors.Join(result, syncSpriteDir(a.root, filepath.Dir(a.canonical)))
}
