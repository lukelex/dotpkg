package appimage

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Spec describes an AppImage package node from the manifest.
type Spec struct {
	Name    string
	Address string
	Target  string
	Sha256  string
}

// Artifact is the resolved, integrity-checked source for an AppImage.
type Artifact struct {
	Address   string
	Algorithm string
	Digest    string
	Version   string
}

// Record is the state needed to identify and safely remove a managed AppImage.
type Record struct {
	Name      string
	Address   string
	Target    string
	Algorithm string
	Digest    string
	Version   string
}

// System is the boundary used by reconciliation. Implementations must not
// install an artifact until Resolve has supplied a verifiable digest.
type System interface {
	Resolve(context.Context, Spec) (Artifact, error)
	Target(Spec) (string, error)
	Installed(context.Context, Record) (bool, error)
	Install(context.Context, Spec, Artifact) (Record, error)
	Remove(context.Context, Record) error
}

// Client downloads and manages user-owned AppImages.
type Client struct {
	HTTP          *http.Client
	HomeDir       string
	GitHubAPIBase string
}

func New() *Client {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return &Client{
		HTTP:          &http.Client{Timeout: 30 * time.Second},
		HomeDir:       home,
		GitHubAPIBase: "https://api.github.com",
	}
}

func (c *Client) Resolve(ctx context.Context, spec Spec) (Artifact, error) {
	u, err := parseAddress(spec.Address)
	if err != nil {
		return Artifact{}, err
	}
	artifact := Artifact{Address: u.String()}
	if spec.Sha256 != "" {
		_, digest, err := parseDigest(spec.Sha256, "sha256")
		if err != nil {
			return Artifact{}, fmt.Errorf("invalid pinned SHA256 for %s: %w", spec.Name, err)
		}
		artifact.Algorithm = "sha256"
		artifact.Digest = digest
		return artifact, nil
	}
	var resolutionErrors []string
	if owner, repository, tag, asset, ok := githubReleaseAddress(u); ok {
		digest, found, resolveErr := c.githubDigest(ctx, owner, repository, tag, asset)
		if resolveErr != nil {
			resolutionErrors = append(resolutionErrors, resolveErr.Error())
		}
		if found {
			artifact.Algorithm = "sha256"
			artifact.Digest = digest
			artifact.Version = tag
			return artifact, nil
		}
	}
	for _, sidecar := range []string{u.String() + ".sha256", u.String() + ".sha256sum"} {
		digest, algorithm, found, sidecarErr := c.sidecarDigest(ctx, sidecar)
		if sidecarErr != nil {
			resolutionErrors = append(resolutionErrors, sidecarErr.Error())
			continue
		}
		if found {
			artifact.Algorithm = algorithm
			artifact.Digest = digest
			return artifact, nil
		}
	}
	if digest, algorithm, found, zsyncErr := c.zsyncDigest(ctx, u.String()+".zsync"); zsyncErr != nil {
		resolutionErrors = append(resolutionErrors, zsyncErr.Error())
	} else if found {
		artifact.Algorithm = algorithm
		artifact.Digest = digest
		return artifact, nil
	}
	if len(resolutionErrors) > 0 {
		return Artifact{}, fmt.Errorf("no verifiable AppImage digest for %s: %s", spec.Name, strings.Join(resolutionErrors, "; "))
	}
	return Artifact{}, fmt.Errorf("no verifiable AppImage digest for %s; expected a GitHub release digest, .sha256 sidecar, or .zsync metadata", spec.Name)
}

func (c *Client) Installed(_ context.Context, record Record) (bool, error) {
	if err := c.validateTarget(record.Target); err != nil {
		return false, err
	}
	file, err := os.Open(record.Target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open AppImage %s: %w", record.Name, err)
	}
	defer file.Close()
	digest, err := hashReader(file, record.Algorithm)
	if err != nil {
		return false, err
	}
	return digest == record.Digest, nil
}

func (c *Client) Target(spec Spec) (string, error) {
	return c.target(spec)
}

func (c *Client) Install(ctx context.Context, spec Spec, artifact Artifact) (Record, error) {
	address, err := parseAddress(artifact.Address)
	if err != nil {
		return Record{}, err
	}
	artifact.Address = address.String()
	target, err := c.target(spec)
	if err != nil {
		return Record{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Record{}, fmt.Errorf("create AppImage directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".dotpkg-appimage-*")
	if err != nil {
		return Record{}, fmt.Errorf("create AppImage temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o755); err != nil {
		temporary.Close()
		return Record{}, fmt.Errorf("set AppImage permissions: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.Address, nil)
	if err != nil {
		temporary.Close()
		return Record{}, fmt.Errorf("create AppImage request: %w", err)
	}
	response, err := c.client().Do(request)
	if err != nil {
		temporary.Close()
		return Record{}, fmt.Errorf("download AppImage %s: %w", spec.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		temporary.Close()
		return Record{}, fmt.Errorf("download AppImage %s: HTTP %s", spec.Name, response.Status)
	}
	digest, err := copyAndHash(temporary, response.Body, artifact.Algorithm)
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Record{}, fmt.Errorf("write AppImage %s: %w", spec.Name, err)
	}
	if digest != artifact.Digest {
		return Record{}, fmt.Errorf("AppImage %s digest mismatch: got %s, want %s:%s", spec.Name, digest, artifact.Algorithm, artifact.Digest)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return Record{}, fmt.Errorf("install AppImage %s: %w", spec.Name, err)
	}
	return Record{
		Name:      spec.Name,
		Address:   artifact.Address,
		Target:    target,
		Algorithm: artifact.Algorithm,
		Digest:    artifact.Digest,
		Version:   artifact.Version,
	}, nil
}

func (c *Client) Remove(ctx context.Context, record Record) error {
	if err := c.validateTarget(record.Target); err != nil {
		return err
	}
	present, err := c.Installed(ctx, record)
	if err != nil {
		return err
	}
	if !present {
		if _, statErr := os.Stat(record.Target); os.IsNotExist(statErr) {
			return nil
		}
		return fmt.Errorf("refusing to remove modified AppImage %s", record.Name)
	}
	if err := os.Remove(record.Target); err != nil {
		return fmt.Errorf("remove AppImage %s: %w", record.Name, err)
	}
	return nil
}

func (c *Client) target(spec Spec) (string, error) {
	target := spec.Target
	if target == "" {
		if c.HomeDir == "" {
			return "", errors.New("cannot determine home directory for AppImage target")
		}
		target = filepath.Join(c.HomeDir, ".local", "bin", spec.Name)
	} else {
		target = os.Expand(target, func(name string) string {
			if name == "HOME" {
				return c.HomeDir
			}
			return os.Getenv(name)
		})
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve AppImage target: %w", err)
	}
	if err := c.validateTarget(target); err != nil {
		return "", err
	}
	return target, nil
}

func (c *Client) validateTarget(target string) error {
	if c.HomeDir == "" {
		return errors.New("cannot determine home directory for AppImage target")
	}
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
		return fmt.Errorf("AppImage target must be inside home directory: %s", target)
	}
	parent := filepath.Dir(target)
	if resolved, resolveErr := filepath.EvalSymlinks(parent); resolveErr == nil {
		resolvedHome, homeErr := filepath.EvalSymlinks(home)
		if homeErr == nil {
			resolvedRelative, relativeErr := filepath.Rel(resolvedHome, resolved)
			if relativeErr != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("AppImage target parent escapes home directory: %s", target)
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

func parseAddress(address string) (*url.URL, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("AppImage address must be an HTTPS URL: %s", address)
	}
	for _, segment := range strings.Split(strings.Trim(parsed.Path, "/"), "/") {
		if strings.EqualFold(segment, "latest") {
			return nil, fmt.Errorf("AppImage address must pin a release, not latest: %s", address)
		}
	}
	path := strings.ToLower(parsed.Path)
	if runtime.GOARCH == "amd64" && containsAny(path, "aarch64", "arm64", "armhf", "armv7") {
		return nil, fmt.Errorf("AppImage address appears to target ARM, but this host is amd64: %s", address)
	}
	if runtime.GOARCH == "arm64" && containsAny(path, "x86_64", "amd64", "i386", "x86") {
		return nil, fmt.Errorf("AppImage address appears to target x86, but this host is arm64: %s", address)
	}
	return parsed, nil
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func githubReleaseAddress(parsed *url.URL) (owner, repository, tag, asset string, ok bool) {
	if parsed.Host != "github.com" {
		return "", "", "", "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[4], parts[5], true
}

type githubRelease struct {
	Assets []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func (c *Client) githubDigest(ctx context.Context, owner, repository, tag, asset string) (string, bool, error) {
	base := c.GitHubAPIBase
	if base == "" {
		base = "https://api.github.com"
	}
	apiURL := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", strings.TrimRight(base, "/"), owner, repository, url.PathEscape(tag))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "dotpkg")
	response, err := c.client().Do(request)
	if err != nil {
		return "", false, fmt.Errorf("query GitHub release metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", false, fmt.Errorf("query GitHub release metadata: HTTP %s", response.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return "", false, fmt.Errorf("parse GitHub release metadata: %w", err)
	}
	for _, candidate := range release.Assets {
		if candidate.Name != asset || candidate.Digest == "" {
			continue
		}
		algorithm, digest, err := parseDigest(candidate.Digest, "sha256")
		if err != nil || algorithm != "sha256" {
			continue
		}
		return digest, true, nil
	}
	return "", false, nil
}

func (c *Client) sidecarDigest(ctx context.Context, address string) (string, string, bool, error) {
	body, found, err := c.fetchOptional(ctx, address)
	if err != nil || !found {
		return "", "", found, err
	}
	algorithm, digest, parseErr := parseDigestText(string(body), "sha256")
	if parseErr != nil {
		return "", "", false, fmt.Errorf("parse checksum sidecar %s: %w", address, parseErr)
	}
	return digest, algorithm, true, nil
}

func (c *Client) zsyncDigest(ctx context.Context, address string) (string, string, bool, error) {
	body, found, err := c.fetchOptional(ctx, address)
	if err != nil || !found {
		return "", "", found, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(parts[0])) {
		case "sha-256", "sha256":
			_, digest, parseErr := parseDigest(parts[1], "sha256")
			if parseErr == nil {
				return digest, "sha256", true, nil
			}
		case "sha-1", "sha1":
			_, digest, parseErr := parseDigest(parts[1], "sha1")
			if parseErr == nil {
				return digest, "sha1", true, nil
			}
		}
	}
	return "", "", false, errors.New("zsync metadata contains no supported whole-file digest")
}

func (c *Client) fetchOptional(ctx context.Context, address string) ([]byte, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, false, err
	}
	response, err := c.client().Do(request)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, false, fmt.Errorf("fetch %s: HTTP %s", address, response.Status)
	}
	body, err := io.ReadAll(response.Body)
	return body, true, err
}

var digestPattern = regexp.MustCompile(`(?i)\b([a-f0-9]{40}|[a-f0-9]{64})\b`)

func parseDigestText(value, defaultAlgorithm string) (string, string, error) {
	return parseDigest(digestPattern.FindString(value), defaultAlgorithm)
}

func parseDigest(value, defaultAlgorithm string) (string, string, error) {
	value = strings.TrimSpace(value)
	algorithm := defaultAlgorithm
	if parts := strings.SplitN(value, ":", 2); len(parts) == 2 {
		algorithm = strings.ToLower(strings.TrimSpace(parts[0]))
		value = strings.TrimSpace(parts[1])
	}
	if algorithm == "sha-256" {
		algorithm = "sha256"
	}
	if algorithm == "sha-1" {
		algorithm = "sha1"
	}
	want := 64
	if algorithm == "sha1" {
		want = 40
	}
	if (algorithm != "sha256" && algorithm != "sha1") || len(value) != want {
		return "", "", fmt.Errorf("unsupported digest %q", value)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", "", fmt.Errorf("invalid digest %q", value)
	}
	return algorithm, strings.ToLower(value), nil
}

func hashReader(reader io.Reader, algorithm string) (string, error) {
	hasher, err := newHash(algorithm)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", fmt.Errorf("hash AppImage: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyAndHash(destination io.Writer, source io.Reader, algorithm string) (string, error) {
	hasher, err := newHash(algorithm)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(io.MultiWriter(destination, hasher), source); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func newHash(algorithm string) (hash.Hash, error) {
	switch algorithm {
	case "sha256":
		return sha256.New(), nil
	case "sha1":
		return sha1.New(), nil
	default:
		return nil, fmt.Errorf("unsupported AppImage digest algorithm %q", algorithm)
	}
}

var _ System = (*Client)(nil)
