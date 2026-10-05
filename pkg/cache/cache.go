package cache

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type PackageRef struct {
	Host    string
	Owner   string
	Repo    string
	Version string
}

var packageURLRegex = regexp.MustCompile(`^https://([^/@]+)/([^@]+)(?:@(.+))?$`)

// IsWellKnownHost reports hosts whose packages are always host/owner/repo.
// Other hosts may use vanity paths (host/repo) or nested groups
// (host/group/subgroup/repo).
func IsWellKnownHost(host string) bool {
	switch host {
	case "github.com", "gitlab.com", "bitbucket.org":
		return true
	}
	return false
}

func ParsePackageURL(rawURL string) (*PackageRef, error) {
	invalid := fmt.Errorf("invalid package URL: %s (expected https://host/owner/repo[@version])", rawURL)
	m := packageURLRegex.FindStringSubmatch(rawURL)
	if m == nil {
		return nil, invalid
	}
	segments := strings.Split(m[2], "/")
	for _, s := range segments {
		if s == "" {
			return nil, invalid
		}
	}
	if IsWellKnownHost(m[1]) && len(segments) != 2 {
		return nil, invalid
	}
	last := len(segments) - 1
	return &PackageRef{
		Host:    m[1],
		Owner:   strings.Join(segments[:last], "/"),
		Repo:    segments[last],
		Version: m[3],
	}, nil
}

func (r *PackageRef) CacheKey() string {
	version := r.Version
	if version == "" {
		version = "_default"
	}
	return filepath.Join(r.Host, r.Owner, r.Repo+"@"+version)
}

func (r *PackageRef) GitURL() string {
	return "https://" + path.Join(r.Host, r.Owner, r.Repo) + ".git"
}

func CacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".ct", "cache"), nil
}

// Resolve returns the local directory for a cached package.
// If the package is not cached, it is downloaded first.
func Resolve(rawURL string) (string, error) {
	ref, err := ParsePackageURL(rawURL)
	if err != nil {
		return "", err
	}

	cacheBase, err := CacheDir()
	if err != nil {
		return "", err
	}

	pkgDir := filepath.Join(cacheBase, ref.CacheKey())

	if dirHasFiles(pkgDir) {
		return pkgDir, nil
	}

	if err := download(ref, pkgDir); err != nil {
		return "", fmt.Errorf("downloading %s: %w", rawURL, err)
	}

	return pkgDir, nil
}

// Invalidate removes cached package directory for the provided package URL.
func Invalidate(rawURL string) error {
	ref, err := ParsePackageURL(rawURL)
	if err != nil {
		return err
	}

	cacheBase, err := CacheDir()
	if err != nil {
		return err
	}

	return os.RemoveAll(filepath.Join(cacheBase, ref.CacheKey()))
}

// cloneFn fetches a package into an empty directory. Tests replace it.
var cloneFn = func(ref *PackageRef, dir string) error {
	args := []string{"clone", "--depth", "1"}
	if ref.Version != "" {
		args = append(args, "--branch", ref.Version)
	}
	args = append(args, ref.GitURL(), dir)

	cmd := exec.Command("git", args...)
	// Fail instead of waiting for a credential prompt nobody may answer
	// (CI, AI agents); credential helpers still work.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s: %s: %w", ref.GitURL(), strings.TrimSpace(string(out)), err)
	}
	return nil
}

// download clones into a temporary sibling of destDir and renames it into
// place, so an interrupted or failed clone never leaves a half-filled cache
// entry that later runs would trust.
func download(ref *PackageRef, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(parent, ".download-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := cloneFn(ref, tmpDir); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(tmpDir, ".git")); err != nil {
		return fmt.Errorf("removing .git: %w", err)
	}

	// An empty directory left behind by an older ct would block the rename.
	if !dirHasFiles(destDir) {
		_ = os.Remove(destDir)
	}
	if err := os.Rename(tmpDir, destDir); err != nil {
		if dirHasFiles(destDir) {
			return nil // a concurrent ct run finished the same download first
		}
		return fmt.Errorf("installing %s into cache: %w", ref.GitURL(), err)
	}
	return nil
}

func dirHasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}
