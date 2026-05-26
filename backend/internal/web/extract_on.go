//go:build embed

package web

import (
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

// ExtractDistTo writes the embedded dist files to targetDir so that an external
// web server (e.g. nginx) can serve them directly from disk.
//
// Behavior:
//   - If targetDir is empty, returns immediately (feature disabled).
//   - Creates targetDir if missing.
//   - Per-file atomic write (write to .tmp then rename) so nginx never observes
//     a half-written file.
//   - Skips files whose on-disk size already matches the embedded size. Vite
//     emits content-hashed filenames for JS/CSS/assets, so equal size implies
//     equal content. Files without hashes (index.html, logo.png, favicon.ico,
//     robots.txt, manifest.json) are always rewritten to pick up updates.
func ExtractDistTo(targetDir string) error {
	if targetDir == "" {
		return nil
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		return err
	}

	var written, skipped int
	err = fs.WalkDir(distFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}

		dst := filepath.Join(targetDir, path)

		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}

		src, err := distFS.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()

		srcInfo, err := src.Stat()
		if err != nil {
			return err
		}

		if shouldSkipIfSameSize(path) {
			if dstInfo, statErr := os.Stat(dst); statErr == nil &&
				!dstInfo.IsDir() && dstInfo.Size() == srcInfo.Size() {
				skipped++
				return nil
			}
		}

		tmp := dst + ".tmp"
		out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, src); err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return err
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		written++
		return nil
	})
	if err != nil {
		return err
	}

	log.Printf("Frontend dist extracted to %s (written=%d, skipped=%d)", targetDir, written, skipped)
	return nil
}

// shouldSkipIfSameSize reports whether a file can be skipped when its on-disk
// size already matches the embedded size. Only safe for files whose name
// changes when their content changes (Vite content-hashed chunks).
func shouldSkipIfSameSize(path string) bool {
	base := filepath.Base(path)
	switch base {
	case "index.html", "logo.png", "favicon.ico", "robots.txt", "manifest.json":
		return false
	}
	return true
}
