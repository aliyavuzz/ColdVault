package archiver

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ZipDirectory compresses the entire directory tree at srcDir into a .zip at dstPath.
// Uses deflate compression. Returns the archive size in bytes.
func ZipDirectory(srcDir, dstPath string) (int64, error) {
	outFile, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer outFile.Close()

	w := zip.NewWriter(outFile)
	defer w.Close()

	srcDir = filepath.Clean(srcDir)

	err = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Build the relative path for the zip entry
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		// Normalize separators to forward slashes for zip spec
		relPath = strings.ReplaceAll(relPath, `\`, `/`)

		if d.IsDir() {
			// Add trailing slash for directory entries
			if relPath != "." {
				_, err = w.Create(relPath + "/")
				return err
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath
		header.Method = zip.Deflate

		writer, err := w.CreateHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(writer, file)
		return err
	})

	if err != nil {
		return 0, err
	}

	// Close writer to flush, then get size
	if err := w.Close(); err != nil {
		return 0, err
	}

	stat, err := outFile.Stat()
	if err != nil {
		return 0, err
	}
	return stat.Size(), nil
}

// Unzip extracts a .zip archive to the destination directory.
func Unzip(srcZip, dstDir string) error {
	r, err := zip.OpenReader(srcZip)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(dstDir, filepath.FromSlash(f.Name))

		// Guard against zip-slip
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dstDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
