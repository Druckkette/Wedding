package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type plannedChallengeMove struct {
	metadataIndex int
	oldKey        string
	newKey        string
	sourcePath    string
	targetPath    string
}

func (s *Server) moveUploadToGallery(metadata uploadMetadata, gallery string) (uploadMetadata, error) {
	if metadata.Gallery == gallery && strings.HasPrefix(filepath.ToSlash(metadata.StoredName), gallery+"/") {
		return metadata, nil
	}
	targetDir := filepath.Join(s.cfg.UploadDir, gallery)
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return metadata, err
	}
	sourcePath, err := s.resolveMediaPath(metadata.StoredName)
	if err != nil {
		return metadata, err
	}
	s.filenameMu.Lock()
	filename, targetPath, err := availableFilename(targetDir, filepath.Base(sourcePath))
	if err == nil {
		err = os.Rename(sourcePath, targetPath)
	}
	s.filenameMu.Unlock()
	if err != nil {
		return metadata, err
	}
	metadata.StoredName = filepath.ToSlash(filepath.Join(gallery, filename))
	metadata.Gallery = gallery
	return metadata, nil
}

func (s *Server) migrateChallengeGallery() (int, error) {
	items, err := s.readMetadata()
	if err != nil {
		return 0, err
	}
	hasChallenge := false
	for _, item := range items {
		if item.IsChallenge {
			hasChallenge = true
			break
		}
	}
	if !hasChallenge {
		return 0, nil
	}
	targetDir := filepath.Join(s.cfg.UploadDir, challengeGalleryName)
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return 0, err
	}
	reserved := make(map[string]bool)
	targetEntries, err := os.ReadDir(targetDir)
	if err != nil {
		return 0, err
	}
	for _, entry := range targetEntries {
		reserved[strings.ToLower(entry.Name())] = true
	}

	plans := make([]plannedChallengeMove, 0)
	metadataChanged := false
	for index := range items {
		item := &items[index]
		if !item.IsChallenge {
			continue
		}
		relative := filepath.ToSlash(item.StoredName)
		if strings.HasPrefix(relative, challengeGalleryName+"/") {
			if item.Gallery != challengeGalleryName {
				item.Gallery = challengeGalleryName
				metadataChanged = true
			}
			continue
		}
		sourcePath, sourceErr := s.metadataSourcePath(relative)
		if sourceErr != nil {
			if errors.Is(sourceErr, os.ErrNotExist) {
				continue
			}
			return 0, sourceErr
		}
		filename := availableReservedFilename(filepath.Base(sourcePath), reserved)
		newKey := filepath.ToSlash(filepath.Join(challengeGalleryName, filename))
		plans = append(plans, plannedChallengeMove{
			metadataIndex: index,
			oldKey:        s.storageKey(relative),
			newKey:        newKey,
			sourcePath:    sourcePath,
			targetPath:    filepath.Join(targetDir, filename),
		})
	}

	renames := make(map[string]string, len(plans))
	for _, plan := range plans {
		renames[plan.oldKey] = plan.newKey
	}
	if err := s.settings.copyMediaStatuses(renames); err != nil {
		return 0, err
	}
	moved := make([]plannedChallengeMove, 0, len(plans))
	rollback := func() {
		for index := len(moved) - 1; index >= 0; index-- {
			_ = os.Rename(moved[index].targetPath, moved[index].sourcePath)
		}
	}
	for _, plan := range plans {
		if err := os.Rename(plan.sourcePath, plan.targetPath); err != nil {
			rollback()
			return 0, err
		}
		moved = append(moved, plan)
		items[plan.metadataIndex].StoredName = plan.newKey
		items[plan.metadataIndex].Gallery = challengeGalleryName
		metadataChanged = true
	}
	if metadataChanged {
		if err := s.replaceMetadata(items); err != nil {
			rollback()
			return 0, err
		}
	}
	return len(moved), nil
}

func (s *Server) metadataSourcePath(relative string) (string, error) {
	if strings.Contains(relative, "/") {
		return s.resolveMediaPath(relative)
	}
	path := filepath.Join(s.cfg.UploadDir, filepath.Base(relative))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", os.ErrNotExist
	}
	return path, nil
}

func availableReservedFilename(requested string, reserved map[string]bool) string {
	base := strings.TrimSuffix(requested, filepath.Ext(requested))
	ext := filepath.Ext(requested)
	for index := 1; ; index++ {
		name := requested
		if index > 1 {
			name = fmt.Sprintf("%s (%d)%s", base, index, ext)
		}
		key := strings.ToLower(name)
		if !reserved[key] {
			reserved[key] = true
			return name
		}
	}
}

func (s *Server) replaceMetadata(items []uploadMetadata) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	path := filepath.Join(s.cfg.UploadDir, "uploads.jsonl")
	temp, err := os.CreateTemp(s.cfg.UploadDir, ".metadata-migrate-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	encoder := json.NewEncoder(temp)
	for _, item := range items {
		if err := encoder.Encode(item); err != nil {
			temp.Close()
			return err
		}
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o640); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
