package host

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func writeFileAtomic(path string, data []byte, perm fs.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := file.Name()
	defer func() {
		if retErr != nil {
			_ = file.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := file.Chmod(perm); err != nil {
		return fmt.Errorf("set state file permissions: %w", err)
	}
	if err := writeAll(file, data); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync state file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("short write")
		}
		data = data[n:]
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
