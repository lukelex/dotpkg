// Package github installs explicitly declared files from pinned GitHub archives.
package github

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type File struct {
	Source string
	Target string
}

type Spec struct {
	Name    string
	Repo    string
	Ref     string
	Archive string
	Sha256  string
	Files   []File
}

type Artifact struct {
	Address string
	Repo    string
	Ref     string
	Archive string
	Sha256  string
}

type InstalledFile struct {
	Source string
	Target string
	Digest string
}

type Record struct {
	Name    string
	Repo    string
	Ref     string
	Archive string
	Sha256  string
	Files   []InstalledFile
}

type System interface {
	Resolve(context.Context, Spec) (Artifact, error)
	Installed(context.Context, Record) (bool, error)
	Install(context.Context, Spec, Artifact, *Record) (Record, error)
	Remove(context.Context, Record) error
}

type Client struct {
	HTTP       *http.Client
	HomeDir    string
	GitHubBase string
}

func New() *Client {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, HomeDir: home, GitHubBase: "https://github.com"}
}

func (c *Client) Resolve(_ context.Context, spec Spec) (Artifact, error) {
	if err := validateSpec(spec); err != nil {
		return Artifact{}, err
	}
	base := strings.TrimRight(c.GitHubBase, "/")
	if base == "" {
		base = "https://github.com"
	}
	address := base + "/" + spec.Repo
	if spec.Archive == "source" {
		address += "/archive/" + spec.Ref + ".tar.gz"
	} else {
		address += "/releases/download/" + spec.Ref + "/" + spec.Archive
	}
	return Artifact{Address: address, Repo: spec.Repo, Ref: spec.Ref, Archive: spec.Archive, Sha256: strings.ToLower(spec.Sha256)}, nil
}

func (c *Client) Installed(_ context.Context, record Record) (bool, error) {
	if err := validateRecord(record); err != nil {
		return false, err
	}
	for _, file := range record.Files {
		if err := c.validateTarget(file.Target); err != nil {
			return false, err
		}
		info, err := os.Lstat(file.Target)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("stat GitHub file %s: %w", file.Target, err)
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
		digest, err := fileDigest(file.Target)
		if err != nil {
			return false, err
		}
		if digest != file.Digest {
			return false, nil
		}
	}
	return true, nil
}

func (c *Client) Install(ctx context.Context, spec Spec, artifact Artifact, previous *Record) (Record, error) {
	if err := validateSpec(spec); err != nil {
		return Record{}, err
	}
	if artifact.Address == "" || artifact.Sha256 == "" {
		return Record{}, errors.New("GitHub artifact is not resolved")
	}
	archive, err := c.download(ctx, spec, artifact)
	if err != nil {
		return Record{}, err
	}
	defer os.Remove(archive)
	extraction, err := os.MkdirTemp("", ".dotpkg-github-extract-*")
	if err != nil {
		return Record{}, fmt.Errorf("create GitHub extraction directory: %w", err)
	}
	defer os.RemoveAll(extraction)
	selected, err := extractArchive(archive, extraction, spec.Files)
	if err != nil {
		return Record{}, fmt.Errorf("extract GitHub artifact %s: %w", spec.Name, err)
	}
	if previous != nil {
		present, err := c.Installed(ctx, *previous)
		if err != nil {
			return Record{}, err
		}
		if !present {
			return Record{}, fmt.Errorf("refusing to update modified GitHub artifact %s", spec.Name)
		}
	}
	previousTargets := make(map[string]struct{})
	if previous != nil {
		for _, file := range previous.Files {
			previousTargets[file.Target] = struct{}{}
		}
	}
	for _, file := range spec.Files {
		if err := c.validateTarget(file.Target); err != nil {
			return Record{}, err
		}
		target := expandHome(file.Target, c.HomeDir)
		if _, err := os.Lstat(target); err == nil {
			if _, managed := previousTargets[target]; !managed {
				return Record{}, fmt.Errorf("refusing to overwrite unmanaged GitHub target: %s", file.Target)
			}
		} else if !os.IsNotExist(err) {
			return Record{}, fmt.Errorf("stat GitHub target %s: %w", file.Target, err)
		}
	}
	record := Record{Name: spec.Name, Repo: spec.Repo, Ref: spec.Ref, Archive: spec.Archive, Sha256: artifact.Sha256, Files: make([]InstalledFile, 0, len(spec.Files))}
	for _, mapping := range spec.Files {
		entry := selected[mapping.Source]
		target := expandHome(mapping.Target, c.HomeDir)
		if err := c.validateTarget(target); err != nil {
			return Record{}, err
		}
		if err := installFile(target, entry.contents, entry.mode); err != nil {
			return Record{}, fmt.Errorf("install GitHub file %s: %w", mapping.Target, err)
		}
		digest := sha256.Sum256(entry.contents)
		record.Files = append(record.Files, InstalledFile{Source: mapping.Source, Target: target, Digest: hex.EncodeToString(digest[:])})
	}
	sort.Slice(record.Files, func(i, j int) bool { return record.Files[i].Target < record.Files[j].Target })
	return record, nil
}

func (c *Client) Remove(ctx context.Context, record Record) error {
	present, err := c.Installed(ctx, record)
	if err != nil {
		return err
	}
	if !present {
		for _, file := range record.Files {
			if _, err := os.Lstat(file.Target); os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("refusing to remove modified GitHub file %s", file.Target)
		}
		return nil
	}
	for _, file := range record.Files {
		if err := os.Remove(file.Target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove GitHub file %s: %w", file.Target, err)
		}
	}
	return nil
}

func (c *Client) download(ctx context.Context, spec Spec, artifact Artifact) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.Address, nil)
	if err != nil {
		return "", fmt.Errorf("create GitHub download request: %w", err)
	}
	response, err := c.client().Do(request)
	if err != nil {
		return "", fmt.Errorf("download GitHub artifact %s: %w", spec.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download GitHub artifact %s: HTTP %s", spec.Name, response.Status)
	}
	file, err := os.CreateTemp("", ".dotpkg-github-*")
	if err != nil {
		return "", fmt.Errorf("create GitHub archive temporary file: %w", err)
	}
	name := file.Name()
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, digest), response.Body)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(name)
		if copyErr != nil {
			return "", fmt.Errorf("write GitHub artifact %s: %w", spec.Name, copyErr)
		}
		return "", fmt.Errorf("close GitHub artifact %s: %w", spec.Name, closeErr)
	}
	got := hex.EncodeToString(digest.Sum(nil))
	if got != strings.ToLower(artifact.Sha256) {
		os.Remove(name)
		return "", fmt.Errorf("GitHub artifact %s digest mismatch: got %s, want sha256:%s", spec.Name, got, artifact.Sha256)
	}
	return name, nil
}

func (c *Client) validateTarget(target string) error {
	if c.HomeDir == "" {
		return errors.New("cannot determine home directory for GitHub target")
	}
	target = expandHome(target, c.HomeDir)
	home, err := filepath.Abs(c.HomeDir)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(home, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("GitHub target must be inside home directory: %s", target)
	}
	parent := filepath.Dir(target)
	if resolved, resolveErr := filepath.EvalSymlinks(parent); resolveErr == nil {
		resolvedHome, homeErr := filepath.EvalSymlinks(home)
		if homeErr == nil {
			resolvedRelative, relativeErr := filepath.Rel(resolvedHome, resolved)
			if relativeErr != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("GitHub target parent escapes home directory: %s", target)
			}
		}
	}
	return nil
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

type archiveEntry struct {
	contents []byte
	mode     os.FileMode
}

func extractArchive(archive, destination string, requested []File) (map[string]archiveEntry, error) {
	entries, err := readArchive(archive, requested)
	if err != nil {
		return nil, err
	}
	for source, entry := range entries {
		output := filepath.Join(destination, filepath.FromSlash(source))
		if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(output, entry.contents, entry.mode.Perm()); err != nil {
			return nil, err
		}
		contents, err := os.ReadFile(output)
		if err != nil {
			return nil, err
		}
		entries[source] = archiveEntry{contents: contents, mode: entry.mode}
	}
	return entries, nil
}

func readArchive(name string, requested []File) (map[string]archiveEntry, error) {
	contents, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(contents, []byte("PK\x03\x04")) {
		return readZip(name, requested)
	}
	reader := bytes.NewReader(contents)
	if len(contents) >= 2 && contents[0] == 0x1f && contents[1] == 0x8b {
		gzipReader, err := gzip.NewReader(reader)
		if err != nil {
			return nil, err
		}
		defer gzipReader.Close()
		return readTar(tar.NewReader(gzipReader), requested)
	}
	return readTar(tar.NewReader(reader), requested)
}

func readTar(reader *tar.Reader, requested []File) (map[string]archiveEntry, error) {
	entries := make(map[string]archiveEntry)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name, err := archiveName(header.Name)
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return nil, fmt.Errorf("archive contains a symlink: %s", name)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		if !header.FileInfo().Mode().IsRegular() {
			return nil, fmt.Errorf("archive contains a non-regular file: %s", name)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		entries[name] = archiveEntry{contents: body, mode: header.FileInfo().Mode()}
	}
	return selectEntries(entries, requested)
}

func readZip(name string, requested []File) (map[string]archiveEntry, error) {
	reader, err := zip.OpenReader(name)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	entries := make(map[string]archiveEntry)
	for _, file := range reader.File {
		entryName, err := archiveName(file.Name)
		if err != nil {
			return nil, err
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("archive contains a symlink: %s", entryName)
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if !mode.IsRegular() {
			return nil, fmt.Errorf("archive contains a non-regular file: %s", entryName)
		}
		body, err := file.Open()
		if err != nil {
			return nil, err
		}
		contents, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		entries[entryName] = archiveEntry{contents: contents, mode: mode}
	}
	return selectEntries(entries, requested)
}

func archiveName(value string) (string, error) {
	value = strings.TrimSuffix(strings.ReplaceAll(value, "\\", "/"), "/")
	if value == "" {
		return "", errors.New("archive contains an empty path")
	}
	if path.IsAbs(value) || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") || value == ".." {
		return "", fmt.Errorf("archive path traversal: %s", value)
	}
	return value, nil
}

func selectEntries(entries map[string]archiveEntry, requested []File) (map[string]archiveEntry, error) {
	roots := make(map[string]struct{})
	for name := range entries {
		root, _, _ := strings.Cut(name, "/")
		roots[root] = struct{}{}
	}
	if len(roots) != 1 {
		return nil, errors.New("archive must contain one top-level directory")
	}
	var root string
	for root = range roots {
	}
	result := make(map[string]archiveEntry, len(requested))
	for _, mapping := range requested {
		entry, ok := entries[root+"/"+mapping.Source]
		if !ok {
			return nil, fmt.Errorf("declared archive file is missing: %s", mapping.Source)
		}
		result[mapping.Source] = entry
	}
	return result, nil
}

func installFile(target string, contents []byte, mode os.FileMode) error {
	if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace non-regular target: %s", target)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".dotpkg-github-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode.Perm()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, target)
}

func fileDigest(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func expandHome(value, home string) string {
	return os.Expand(value, func(name string) string {
		if name == "HOME" {
			return home
		}
		return os.Getenv(name)
	})
}

func validateSpec(spec Spec) error {
	if spec.Name == "" || spec.Repo == "" || spec.Ref == "" || spec.Archive == "" || len(spec.Files) == 0 {
		return errors.New("incomplete GitHub artifact specification")
	}
	parts := strings.Split(spec.Repo, "/")
	if len(parts) != 2 || !safeGitHubComponent(parts[0]) || !safeGitHubComponent(parts[1]) || !safeGitHubComponent(spec.Ref) {
		return errors.New("invalid GitHub repository or ref")
	}
	if len(spec.Sha256) != 64 {
		return errors.New("GitHub artifact requires a SHA-256 checksum")
	}
	if _, err := hex.DecodeString(spec.Sha256); err != nil {
		return errors.New("GitHub artifact has an invalid SHA-256 checksum")
	}
	if spec.Archive != "source" && (strings.Contains(spec.Archive, "/") || strings.Contains(spec.Archive, "\\") || spec.Archive == "." || spec.Archive == "..") {
		return errors.New("GitHub release asset must be a file name")
	}
	for _, file := range spec.Files {
		if !safeRelativeFile(file.Source) || file.Target == "" {
			return errors.New("GitHub artifact has an unsafe file mapping")
		}
	}
	return nil
}

func safeGitHubComponent(value string) bool {
	if value == "" || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func safeRelativeFile(value string) bool {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validateRecord(record Record) error {
	if record.Name == "" || record.Repo == "" || record.Ref == "" || record.Archive == "" || len(record.Sha256) != 64 || len(record.Files) == 0 {
		return errors.New("invalid managed GitHub artifact record")
	}
	for _, file := range record.Files {
		if file.Source == "" || file.Target == "" || len(file.Digest) != 64 {
			return errors.New("invalid managed GitHub artifact file record")
		}
	}
	return nil
}

var _ System = (*Client)(nil)
