package maintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type Artifact struct {
	Build  string
	Path   string
	SHA256 string
}

type ArtifactPair struct {
	Target   Artifact
	Rollback Artifact
}

// StageArtifactPair copies the target and exact currently-running rollback
// bytes into content-addressed, read-only paths. It never replaces the live
// executable; activation is a later handoff phase. Existing content must match
// its hash exactly, so a partial/tampered prepare fails while the old daemon
// remains active.
func StageArtifactPair(root, targetBuild string, targetData []byte, rollbackBuild, rollbackSource string) (*ArtifactPair, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(rollbackSource) {
		return nil, fmt.Errorf("artifact root and rollback source must be absolute")
	}
	if err := validateBuildID(targetBuild); err != nil {
		return nil, fmt.Errorf("target build: %w", err)
	}
	if err := validateBuildID(rollbackBuild); err != nil {
		return nil, fmt.Errorf("rollback build: %w", err)
	}
	if len(targetData) == 0 {
		return nil, fmt.Errorf("target artifact is empty")
	}
	rollbackData, err := os.ReadFile(rollbackSource)
	if err != nil {
		return nil, fmt.Errorf("read rollback executable: %w", err)
	}
	if len(rollbackData) == 0 {
		return nil, fmt.Errorf("rollback artifact is empty")
	}
	rollback, err := stageArtifact(root, rollbackBuild, rollbackData)
	if err != nil {
		return nil, fmt.Errorf("stage rollback artifact: %w", err)
	}
	target, err := stageArtifact(root, targetBuild, targetData)
	if err != nil {
		return nil, fmt.Errorf("stage target artifact: %w", err)
	}
	return &ArtifactPair{Target: target, Rollback: rollback}, nil
}

func validateBuildID(build string) error {
	if build == "" || build != strings.TrimSpace(build) || len(build) > 128 {
		return fmt.Errorf("build identity is invalid")
	}
	for _, r := range build {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("build identity is invalid")
	}
	return nil
}

func stageArtifact(root, build string, data []byte) (Artifact, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(root, "versions", build, hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(dir, "no-mistakes")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 {
			return Artifact{}, fmt.Errorf("immutable artifact %s has unsafe file type or mode", path)
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return Artifact{}, readErr
		}
		existingSum := sha256.Sum256(existing)
		if hex.EncodeToString(existingSum[:]) != hash {
			return Artifact{}, fmt.Errorf("immutable artifact %s does not match its content address", path)
		}
		return Artifact{Build: build, Path: path, SHA256: hash}, nil
	} else if !os.IsNotExist(err) {
		return Artifact{}, err
	}
	tmp, err := os.CreateTemp(dir, ".no-mistakes-stage-*")
	if err != nil {
		return Artifact{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return Artifact{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Artifact{}, err
	}
	if err := tmp.Chmod(0o555); err != nil {
		_ = tmp.Close()
		return Artifact{}, err
	}
	if err := tmp.Close(); err != nil {
		return Artifact{}, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil {
			existingSum := sha256.Sum256(existing)
			if hex.EncodeToString(existingSum[:]) == hash {
				return Artifact{Build: build, Path: path, SHA256: hash}, nil
			}
		}
		return Artifact{}, err
	}
	return Artifact{Build: build, Path: path, SHA256: hash}, nil
}
