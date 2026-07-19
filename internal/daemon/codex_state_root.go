package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"

	"github.com/kunchenguid/no-mistakes/internal/db"
)

// CodexStateRootUnavailable is the only error exposed for an invalid selected
// root or for lost process-local custody during recovery. It deliberately
// contains neither the selected value nor validation details.
const CodexStateRootUnavailable = "selected Codex state root is unavailable"

const codexStateRootUnavailable = CodexStateRootUnavailable

// normalizeSelectedCodexStateRoot validates filesystem metadata only. It does
// not open, enumerate, read, copy, hash, or mutate anything inside the root.
func normalizeSelectedCodexStateRoot(selected *string) (*string, error) {
	if selected == nil {
		return nil, nil
	}
	raw := *selected
	if raw == "" || raw != strings.TrimSpace(raw) || !filepath.IsAbs(raw) || containsControl(raw) {
		return nil, errors.New(codexStateRootUnavailable)
	}

	cleaned := filepath.Clean(raw)
	separator := string(filepath.Separator)
	volume := filepath.VolumeName(raw)
	remainder := strings.TrimPrefix(raw, volume)
	for strings.HasPrefix(remainder, separator) {
		remainder = strings.TrimPrefix(remainder, separator)
	}
	current := volume + separator
	if info, err := os.Lstat(current); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New(codexStateRootUnavailable)
	}
	for _, component := range strings.Split(remainder, separator) {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			current = filepath.Dir(current)
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New(codexStateRootUnavailable)
		}
	}
	rootInfo, err := os.Lstat(raw)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New(codexStateRootUnavailable)
	}
	permissionBits := rootInfo.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if !rootInfo.IsDir() || permissionBits != 0o700 || !ownedByEffectiveUser(rootInfo) {
		return nil, errors.New(codexStateRootUnavailable)
	}
	return &cleaned, nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// ownedByEffectiveUser uses only the ownership metadata already returned by
// Lstat. Stat_t is platform-specific, so read its conventional Uid field
// reflectively and fail closed on platforms that cannot supply it.
func ownedByEffectiveUser(info os.FileInfo) bool {
	if info == nil || info.Sys() == nil {
		return false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	uid := value.FieldByName("Uid")
	if !uid.IsValid() {
		return false
	}
	var owner int64
	switch uid.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		owner = uid.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value := uid.Uint()
		if value > uint64(^uint(0)>>1) {
			return false
		}
		owner = int64(value)
	default:
		return false
	}
	return owner == int64(os.Geteuid())
}

// codexStateRootForRun prevents recovery from replacing lost per-run custody
// with the daemon's ambient CODEX_HOME.
func codexStateRootForRun(run *db.Run, selected *string) (string, error) {
	if selected != nil && *selected != "" {
		return *selected, nil
	}
	if run != nil && run.RequiresCodexStateRoot {
		return "", errors.New(codexStateRootUnavailable)
	}
	return "", nil
}
