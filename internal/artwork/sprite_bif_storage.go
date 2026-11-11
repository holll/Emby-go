package artwork

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// bifPath derives the immutable rendition name from the indexed NFO, not STRM.
// Keep all filesystem operations anchored to the already verified library root.
func (s *spriteSource) bifPath(generation string) (string, error) {
	nfo := s.nfoRelative
	name := "." + strings.TrimSuffix(filepath.Base(nfo), filepath.Ext(nfo)) + "-" + generation + ".bif"
	path := name
	if err := safe(s.nfoRoot, path); err != nil {
		return "", err
	}
	return path, nil
}

func readSpriteBIFAsset(ctx context.Context, source *spriteSource, r *spriteRoot, m spriteDiskManifest) ([]byte, error) {
	if m.NFOPath != "" && m.NFOPath != source.movie.NFOPath {
		return nil, ErrSpriteChanged
	}
	path, err := source.bifPath(m.Generation)
	if err != nil {
		return nil, err
	}
	if err = source.verify(ctx); err != nil {
		return nil, err
	}
	b, err := readLimited(source.nfoRoot, path, spriteMaxBytes)
	if os.IsNotExist(err) {
		// Previously published generations remain readable until the next task moves
		// their active BIF without extracting any frames.
		return readSpriteAsset(ctx, r, m, "index.bif", m.BIFBytes, m.BIFHash, spriteMaxBytes)
	}
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != m.BIFBytes || spriteSum(b) != m.BIFHash {
		return nil, ErrSpriteChanged
	}
	return b, ctx.Err()
}

// removeSpriteBIF prunes only the two known names for a validated generation.
func removeSpriteBIF(source *spriteSource, generation string) error {
	path, err := source.bifPath(generation)
	if err != nil {
		return err
	}
	for _, name := range []string{path, path + ".tmp"} {
		if err = safe(source.nfoRoot, name); err != nil {
			return err
		}
		info, err := source.nfoRoot.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("BIF cleanup target is not a regular file")
		}
		if err = source.nfoRoot.Remove(name); err != nil {
			return err
		}
	}
	return syncSpriteDir(source.nfoRoot, filepath.Dir(path))
}

// prepareBIFSidecar protects pre-existing independent files unless the caller
// explicitly requested overwrite or proved the previous published hash.
func (s *spriteSource) prepareBIFSidecar(m spriteDiskManifest, oldHash string, overwrite bool) (*spriteBIFAlias, error) {
	canonical := strings.TrimSuffix(filepath.Base(s.nfoRelative), filepath.Ext(s.nfoRelative)) + ".bif"
	current, err := readLimited(s.nfoRoot, canonical, spriteMaxBytes)
	if err == nil {
		hash := spriteSum(current)
		if !overwrite && hash != m.BIFHash && hash != oldHash {
			return nil, errors.New("existing BIF sidecar differs; explicit overwrite required")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	backing, err := s.bifPath(m.Generation)
	if err != nil {
		return nil, err
	}
	return prepareSpriteBIFAlias(s.nfoRoot, backing, canonical)
}

// migrateSpriteBIF runs under the existing movie lock. The old file is removed
// only after the same validated bytes have been synced and atomically renamed.
func migrateSpriteBIF(ctx context.Context, source *spriteSource, r *spriteRoot, m spriteDiskManifest) (err error) {
	path, err := source.bifPath(m.Generation)
	if err != nil {
		return err
	}
	b, err := readSpriteBIFAsset(ctx, source, r, m)
	if err != nil {
		return err
	}
	if err = validateSpriteBIF(b, m.SpriteManifest); err != nil {
		return err
	}
	if _, err = source.nfoRoot.Lstat(path); os.IsNotExist(err) {
		temp := path + ".tmp"
		if err = spriteWrite(source.nfoRoot, temp, b); err != nil {
			return err
		}
		defer source.nfoRoot.Remove(temp)
		if err = source.verify(ctx); err != nil {
			return err
		}
		if err = safe(source.nfoRoot, path); err != nil {
			return err
		}
		if err = source.nfoRoot.Rename(temp, path); err != nil {
			return err
		}
		if err = syncSpriteDir(source.nfoRoot, filepath.Dir(path)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	alias, err := source.prepareBIFSidecar(m, "", false)
	if err != nil {
		return err
	}
	committed := false
	defer func() { err = errors.Join(err, alias.finish(committed)) }()
	if err = source.verify(ctx); err != nil {
		return err
	}
	if err = r.verify(); err != nil {
		return err
	}
	if err = alias.commit(); err != nil {
		return err
	}
	if m.NFOPath == "" {
		m.NFOPath = source.movie.NFOPath
		raw, e := json.Marshal(m)
		if e != nil {
			return e
		}
		manifest := filepath.Join(spriteGenerationDir(m.MovieID, m.Generation), "manifest.json")
		temp := manifest + ".tmp"
		if err = spriteWrite(r.root, temp, raw); err != nil {
			return err
		}
		defer r.root.Remove(temp)
		if err = r.root.Rename(temp, manifest); err != nil {
			return err
		}
	}
	legacy := filepath.Join(spriteGenerationDir(m.MovieID, m.Generation), "index.bif")
	if err = safe(r.root, legacy); err != nil {
		return err
	}
	if err = r.root.Remove(legacy); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = syncSpriteDir(r.root, spriteGenerationDir(m.MovieID, m.Generation)); err != nil {
		return err
	}
	committed = true
	return nil
}
