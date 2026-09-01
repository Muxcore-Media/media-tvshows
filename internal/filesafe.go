package internal

import (
	"os"
	"path/filepath"
	"strings"
)

func isFileUnderRoot(filePath, rootFolder string) bool {
	if filePath == "" {
		return false
	}
	cleaned := filepath.Clean(filePath)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	root := filepath.Clean(rootFolder)
	if root == "" || root == "." {
		return false
	}
	sep := string(os.PathSeparator)
	return cleaned == root || strings.HasPrefix(cleaned, root+sep)
}

func safeDeleteMediaFile(filePath, rootFolder string) error {
	if filePath == "" {
		return nil
	}
	cleaned := filepath.Clean(filePath)
	if !filepath.IsAbs(cleaned) {
		return nil
	}
	root := filepath.Clean(rootFolder)
	if root == "" || root == "." {
		return nil
	}
	if !isFileUnderRoot(cleaned, root) {
		return nil
	}
	if err := os.Remove(cleaned); err != nil && !os.IsNotExist(err) {
		return err
	}
	removeEmptyParents(cleaned, root)
	return nil
}

func removeEmptyParents(filePath, rootFolder string) {
	root := filepath.Clean(rootFolder)
	dir := filepath.Dir(filepath.Clean(filePath))
	sep := string(os.PathSeparator)
	for dir != root && strings.HasPrefix(dir, root+sep) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
