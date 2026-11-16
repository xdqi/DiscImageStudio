package dvd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// .NET-flavored file helpers: error texts match the C# exceptions that
// Program.RunCommand would print.

func fullPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

// translateReadError converts Go file errors to the .NET message text.
func translateReadError(err error, path string) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		if errors.Is(err, fs.ErrNotExist) {
			return &dotnetFileNotFoundError{path: fullPath(path)}
		}
	}
	return err
}

// dotnetFileNotFoundError mirrors FileNotFoundException.Message.
type dotnetFileNotFoundError struct{ path string }

func (e *dotnetFileNotFoundError) Error() string {
	return "Could not find file '" + e.path + "'."
}

// dotnetDirectoryNotFoundError mirrors DirectoryNotFoundException.Message.
type dotnetDirectoryNotFoundError struct{ path string }

func (e *dotnetDirectoryNotFoundError) Error() string {
	return "Could not find a part of the path '" + e.path + "'."
}

func dotnetReadAllBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, translateReadError(err, path)
	}
	return data, nil
}

func dotnetWriteAllBytes(path string, data []byte) error {
	err := os.WriteFile(path, data, 0o666)
	if err != nil {
		return translateWriteError(err, path)
	}
	return nil
}

func dotnetWriteAllText(path string, text string) error {
	return dotnetWriteAllBytes(path, []byte(text))
}

func translateWriteError(err error, path string) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && errors.Is(err, fs.ErrNotExist) {
		return &dotnetDirectoryNotFoundError{path: fullPath(path)}
	}
	return err
}

func ensureParentDirectory(path string) error {
	directory := filepath.Dir(fullPath(path))
	if directory != "" {
		if err := os.MkdirAll(directory, 0o777); err != nil {
			return err
		}
	}
	return nil
}

// pathEquals mirrors the OrdinalIgnoreCase full-path comparisons.
func pathEquals(a, b string) bool {
	return strings.EqualFold(fullPath(a), fullPath(b))
}

func fileLength(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, translateReadError(err, path)
	}
	return info.Size(), nil
}
