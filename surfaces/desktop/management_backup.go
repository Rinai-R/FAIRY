package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fairy/app/edge"
	"github.com/spf13/fileflow"
	"github.com/spf13/pathologize"
)

type ManagementBackup struct {
	Path            string `json:"path"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
	FileCount       int    `json:"fileCount"`
}

func createSeekDBBackup(source edge.BackupSource) (ManagementBackup, error) {
	if strings.TrimSpace(source.ConfigRoot) == "" || strings.TrimSpace(source.DataDir) == "" {
		return ManagementBackup{}, edge.ErrBackupDataDirRequired
	}
	info, err := os.Stat(source.DataDir)
	if err != nil {
		return ManagementBackup{}, fmt.Errorf("reading SeekDB data directory: %w", err)
	}
	if !info.IsDir() {
		return ManagementBackup{}, fmt.Errorf("SeekDB data directory is not a directory")
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	destination := pathologize.Join(source.ConfigRoot, "backups", stamp)
	skipPrefix := destination + string(os.PathSeparator)
	createdAt := time.Now().UnixMilli()
	flow := fileflow.Flow{DirMode: 0o700}
	copied := 0
	err = filepath.WalkDir(source.DataDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == destination || strings.HasPrefix(path, skipPrefix) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(source.DataDir, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		target := pathologize.Join(destination, parts...)
		if _, err := flow.Copy(path, target); err != nil {
			return fmt.Errorf("copying %s: %w", rel, err)
		}
		copied++
		return nil
	})
	if err != nil {
		return ManagementBackup{}, err
	}
	if err := os.Chmod(destination, 0o700); err != nil {
		return ManagementBackup{}, fmt.Errorf("securing backup directory: %w", err)
	}
	return ManagementBackup{Path: destination, CreatedAtUnixMS: createdAt, FileCount: copied}, nil
}
