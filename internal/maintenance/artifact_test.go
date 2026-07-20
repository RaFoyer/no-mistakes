package maintenance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageArtifactPairKeepsImmutableTargetAndExactRollback(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(t.TempDir(), "no-mistakes")
	if err := os.WriteFile(current, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	pair, err := StageArtifactPair(root, "build-new", []byte("new-binary"), "build-old", current)
	if err != nil {
		t.Fatal(err)
	}
	for name, artifact := range map[string]Artifact{"target": pair.Target, "rollback": pair.Rollback} {
		if !filepath.IsAbs(artifact.Path) || artifact.SHA256 == "" {
			t.Fatalf("%s artifact is incomplete: %+v", name, artifact)
		}
		info, err := os.Stat(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o222 != 0 {
			t.Fatalf("%s artifact is writable: %o", name, info.Mode().Perm())
		}
	}
	if got, _ := os.ReadFile(pair.Target.Path); string(got) != "new-binary" {
		t.Fatalf("target content = %q", got)
	}
	if got, _ := os.ReadFile(pair.Rollback.Path); string(got) != "old-binary" {
		t.Fatalf("rollback content = %q", got)
	}

	if err := os.Chmod(pair.Target.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := StageArtifactPair(root, "build-new", []byte("new-binary"), "build-old", current); err == nil {
		t.Fatal("staging should refuse an existing writable artifact even when its bytes match")
	}
	if err := os.WriteFile(pair.Target.Path, []byte("tampered"), 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := StageArtifactPair(root, "build-new", []byte("new-binary"), "build-old", current); err == nil {
		t.Fatal("staging over a mismatched immutable artifact should fail closed")
	}
}

func TestStageArtifactPairPrepareFailureLeavesRollbackSourceUntouched(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(t.TempDir(), "no-mistakes")
	if err := os.WriteFile(current, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := StageArtifactPair(root, "../invalid", []byte("new"), "old", current); err == nil {
		t.Fatal("invalid target build should fail")
	}
	if got, _ := os.ReadFile(current); string(got) != "old-binary" {
		t.Fatalf("prepare failure changed rollback source: %q", got)
	}
}
