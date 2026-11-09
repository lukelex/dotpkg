// Package remotebundle syncs declared GitHub repository paths as one managed
// directory from a single revision. It does not execute downloaded content.
package remotebundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

const markerName = ".dotpkg-bundle.json"
const maxArchive = 16 << 20
const maxExtracted = 8 << 20

var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type System struct {
	client       *http.Client
	api, archive string
}

func New() *System {
	return &System{client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || (req.URL.Hostname() != "api.github.com" && req.URL.Hostname() != "codeload.github.com") {
			return fmt.Errorf("remote bundle redirect to untrusted host")
		}
		if len(via) > 3 {
			return fmt.Errorf("too many remote bundle redirects")
		}
		return nil
	}}, api: "https://api.github.com", archive: "https://codeload.github.com"}
}

type Record struct{ Repository, Target, Commit string }
type Change struct{ Name, Action, Old, New string }
type Plan struct {
	Changes  []Change
	Specs    map[string]manifest.RemoteBundle
	Previous map[string]state.RemoteBundle
}

func (p Plan) Count() int { return len(p.Changes) }

func target(spec manifest.RemoteBundle) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(spec.Target, "$HOME/.local/share/dotpkg/") {
		return "", fmt.Errorf("bundle target must be inside dotpkg's data directory")
	}
	rel := strings.TrimPrefix(spec.Target, "$HOME/.local/share/dotpkg/")
	if !safePath(rel) {
		return "", fmt.Errorf("unsafe bundle destination: %s", spec.Target)
	}
	t := filepath.Join(home, ".local", "share", "dotpkg", filepath.FromSlash(rel))
	for parent := filepath.Dir(t); parent != home && parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return "", fmt.Errorf("unsafe bundle destination parent: %s", parent)
		}
	}
	return t, nil
}

func safePath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (s *System) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("remote bundle response exceeds size limit")
	}
	return data, nil
}

func (s *System) resolve(ctx context.Context, spec manifest.RemoteBundle) (string, error) {
	if spec.Commit != "" {
		return spec.Commit, nil
	}
	data, err := s.get(ctx, s.api+"/repos/"+spec.Repository, 64<<10)
	if err != nil {
		return "", err
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(data, &repo); err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`).MatchString(repo.DefaultBranch) || strings.Contains(repo.DefaultBranch, "..") {
		return "", fmt.Errorf("invalid upstream default branch")
	}
	data, err = s.get(ctx, s.api+"/repos/"+spec.Repository+"/commits/"+repo.DefaultBranch, 64<<10)
	if err != nil {
		return "", err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(data, &commit); err != nil {
		return "", err
	}
	if !shaPattern.MatchString(commit.SHA) {
		return "", fmt.Errorf("invalid upstream commit SHA")
	}
	return commit.SHA, nil
}

type marker struct {
	Repository string   `json:"repository"`
	Commit     string   `json:"commit"`
	Digest     string   `json:"digest"`
	Paths      []string `json:"paths"`
}

func digest(root string, paths []string) (string, error) {
	var files []string
	var directories []string
	allowed := map[string]bool{markerName: true}
	for _, source := range paths {
		allowed[path.Base(source)] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return "", fmt.Errorf("unexpected file in bundle: %s", entry.Name())
		}
	}
	for _, source := range paths {
		base := filepath.Join(root, path.Base(source))
		if err := filepath.WalkDir(base, func(p string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("bundle contains a symlink: %s", p)
			}
			if entry.IsDir() {
				directories = append(directories, p)
			}
			if !entry.IsDir() {
				if !entry.Type().IsRegular() {
					return fmt.Errorf("bundle contains a non-file: %s", p)
				}
				files = append(files, p)
			}
			return nil
		}); err != nil {
			return "", err
		}
	}
	sort.Strings(files)
	sort.Strings(directories)
	h := sha256.New()
	for _, directory := range directories {
		rel, _ := filepath.Rel(root, directory)
		fmt.Fprintf(h, "directory:%s\x00", filepath.ToSlash(rel))
	}
	var total int64
	for _, file := range files {
		rel, _ := filepath.Rel(root, file)
		info, err := os.Lstat(file)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() > maxExtracted-total {
			return "", fmt.Errorf("bundle contains oversized or non-regular file: %s", file)
		}
		total += info.Size()
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func installed(t, repo, sha string, paths []string) (bool, bool, error) {
	info, err := os.Lstat(t)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return true, false, nil
	}
	data, err := os.ReadFile(filepath.Join(t, markerName))
	if err != nil {
		return true, false, nil
	}
	var m marker
	if json.Unmarshal(data, &m) != nil || m.Repository != repo || !shaPattern.MatchString(m.Commit) || !samePaths(m.Paths, paths) {
		return true, false, nil
	}
	d, err := digest(t, paths)
	return true, err == nil && m.Commit == sha && m.Digest == d, nil
}

func owned(t, repo, sha string, paths []string) bool {
	info, err := os.Lstat(t)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	markerInfo, err := os.Lstat(filepath.Join(t, markerName))
	if err != nil || !markerInfo.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(filepath.Join(t, markerName))
	if err != nil {
		return false
	}
	var m marker
	return json.Unmarshal(data, &m) == nil && m.Repository == repo && m.Commit == sha && digestPattern.MatchString(m.Digest) && samePaths(m.Paths, paths)
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *System) Build(ctx context.Context, specs []manifest.RemoteBundle, tracked map[string]state.RemoteBundle) (Plan, error) {
	p := Plan{Specs: map[string]manifest.RemoteBundle{}, Previous: tracked}
	for _, spec := range specs {
		t, err := target(spec)
		if err != nil {
			return p, err
		}
		sha, err := s.resolve(ctx, spec)
		if err != nil {
			return p, fmt.Errorf("resolve %s: %w", spec.Name, err)
		}
		spec.Commit = sha
		p.Specs[spec.Name] = spec
		previous, trackedItem := tracked[spec.Name]
		exists, valid, err := installed(t, spec.Repository, sha, spec.Paths)
		if err != nil {
			return p, err
		}
		if exists && (!trackedItem || previous.Target != t || !owned(t, previous.Repository, previous.Commit, previous.Paths)) {
			return p, fmt.Errorf("remote bundle destination conflicts with user-owned content: %s", t)
		}
		if !valid || !trackedItem || previous.Commit != sha || previous.Repository != spec.Repository || !samePaths(previous.Paths, spec.Paths) {
			p.Changes = append(p.Changes, Change{spec.Name, "install", previous.Commit, sha})
		}
	}
	for name, previous := range tracked {
		if _, ok := p.Specs[name]; !ok {
			p.Changes = append(p.Changes, Change{name, "remove", previous.Commit, ""})
		}
	}
	sort.Slice(p.Changes, func(i, j int) bool { return p.Changes[i].Name < p.Changes[j].Name })
	return p, nil
}

func CleanPlan(specs []manifest.RemoteBundle, tracked map[string]state.RemoteBundle) Plan {
	p := Plan{Specs: map[string]manifest.RemoteBundle{}, Previous: tracked}
	for _, spec := range specs {
		p.Specs[spec.Name] = spec
	}
	for name, previous := range tracked {
		if _, ok := p.Specs[name]; !ok {
			p.Changes = append(p.Changes, Change{name, "remove", previous.Commit, ""})
		}
	}
	sort.Slice(p.Changes, func(i, j int) bool { return p.Changes[i].Name < p.Changes[j].Name })
	return p
}

func (s *System) stage(ctx context.Context, spec manifest.RemoteBundle, parent string) (string, error) {
	data, err := s.get(ctx, s.archive+"/"+spec.Repository+"/tar.gz/"+spec.Commit, maxArchive)
	if err != nil {
		return "", err
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	stage, err := os.MkdirTemp(parent, ".dotpkg-bundle-stage-*")
	if err != nil {
		return "", err
	}
	valid := false
	defer func() {
		if !valid {
			os.RemoveAll(stage)
		}
	}()
	reader := tar.NewReader(gz)
	var total int64
	found := make([]bool, len(spec.Paths))
	for count := 0; ; count++ {
		if count > 20000 {
			return "", fmt.Errorf("archive has too many entries")
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := header.Name
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return "", fmt.Errorf("archive contains link: %s", name)
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || path.Clean(name) != strings.TrimSuffix(name, "/") || strings.Contains("/"+name+"/", "/../") {
			return "", fmt.Errorf("unsafe archive path: %s", name)
		}
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 {
			continue
		}
		member := strings.TrimSuffix(parts[1], "/")
		selected := -1
		for i, source := range spec.Paths {
			if member == source || strings.HasPrefix(member, source+"/") {
				selected = i
				break
			}
		}
		if selected < 0 {
			continue
		}
		found[selected] = true
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return "", fmt.Errorf("unsupported archive entry: %s", name)
		}
		rel := path.Base(spec.Paths[selected]) + strings.TrimPrefix(member, spec.Paths[selected])
		dest := filepath.Join(stage, filepath.FromSlash(rel))
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if header.Size < 0 || header.Size > maxExtracted-total {
			return "", fmt.Errorf("bundle exceeds extracted size limit")
		}
		total += header.Size
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return "", err
		}
		_, err = io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	for i, present := range found {
		if !present {
			return "", fmt.Errorf("requested path missing from archive: %s", spec.Paths[i])
		}
	}
	d, err := digest(stage, spec.Paths)
	if err != nil {
		return "", err
	}
	meta, _ := json.Marshal(marker{spec.Repository, spec.Commit, d, spec.Paths})
	if err := os.WriteFile(filepath.Join(stage, markerName), meta, 0o644); err != nil {
		return "", err
	}
	valid = true
	return stage, nil
}

// Install swaps the requested paths and calls save while the old bundle can still
// be restored. Failure to persist state restores the previous directory.
func (s *System) Install(ctx context.Context, spec manifest.RemoteBundle, previous state.RemoteBundle, save func() error) error {
	t, err := target(spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t), 0o755); err != nil {
		return err
	}
	stage, err := s.stage(ctx, spec, filepath.Dir(t))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	exists, _, err := installed(t, spec.Repository, spec.Commit, spec.Paths)
	if err != nil {
		return err
	}
	if exists && (previous.Target != t || !owned(t, previous.Repository, previous.Commit, previous.Paths)) {
		return fmt.Errorf("remote bundle destination conflicts with user-owned content: %s", t)
	}
	backup := ""
	if exists {
		backup, err = os.MkdirTemp(filepath.Dir(t), ".dotpkg-bundle-old-*")
		if err != nil {
			return err
		}
		os.Remove(backup)
		if err := os.Rename(t, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, t); err != nil {
		if backup != "" {
			_ = os.Rename(backup, t)
		}
		return err
	}
	if err := save(); err != nil {
		if removeErr := os.RemoveAll(t); removeErr != nil {
			return fmt.Errorf("save state: %w; restore blocked: %v", err, removeErr)
		}
		if backup != "" {
			if restoreErr := os.Rename(backup, t); restoreErr != nil {
				return fmt.Errorf("save state: %w; restore failed: %v", err, restoreErr)
			}
		}
		return err
	}
	if backup != "" {
		return os.RemoveAll(backup)
	}
	return nil
}

func Remove(name string, record state.RemoteBundle) error {
	if len(record.Paths) == 0 || len(record.Paths) > 20 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*/[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(record.Repository) || !shaPattern.MatchString(record.Commit) {
		return fmt.Errorf("refusing to clean invalid bundle state %s", name)
	}
	for _, source := range record.Paths {
		if !safePath(source) || path.Base(source) == markerName {
			return fmt.Errorf("refusing to clean unsafe bundle source %s", source)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	base := filepath.Join(home, ".local/share/dotpkg")
	rel, err := filepath.Rel(base, record.Target)
	if err != nil || !safePath(filepath.ToSlash(rel)) || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("refusing to clean unexpected bundle destination %s", record.Target)
	}
	spec := manifest.RemoteBundle{Name: name, Repository: record.Repository, Target: "$HOME/.local/share/dotpkg/" + filepath.ToSlash(rel), Paths: record.Paths}
	t, err := target(spec)
	if err != nil {
		return err
	}
	if record.Target != t {
		return fmt.Errorf("refusing to clean unexpected bundle destination %s", record.Target)
	}
	exists, valid, err := installed(t, record.Repository, record.Commit, record.Paths)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !valid || !owned(t, record.Repository, record.Commit, record.Paths) {
		return fmt.Errorf("refusing to remove modified or unowned bundle %s", t)
	}
	return os.RemoveAll(t)
}
