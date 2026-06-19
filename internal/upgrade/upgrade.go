package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const DefaultRepo = "durandom/token-burn"

type Options struct {
	Repo        string
	Version     string
	Current     string
	BinaryPath  string
	HTTPClient  *http.Client
	InstallOnly bool
	Force       bool
}

type Result struct {
	From       string
	To         string
	BinaryPath string
	Changed    bool
}

type releaseResponse struct {
	TagName string `json:"tag_name"`
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Repo == "" {
		opts.Repo = DefaultRepo
	}
	if opts.Version == "" {
		opts.Version = "latest"
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.BinaryPath == "" {
		path, err := os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("resolve executable path: %w", err)
		}
		opts.BinaryPath = path
	}

	targetVersion := opts.Version
	if targetVersion == "latest" {
		version, err := latestVersion(ctx, opts.HTTPClient, opts.Repo)
		if err != nil {
			return Result{}, err
		}
		targetVersion = version
	}
	if strings.TrimSpace(targetVersion) == "" {
		return Result{}, errors.New("target version is empty")
	}

	result := Result{From: opts.Current, To: targetVersion, BinaryPath: opts.BinaryPath}
	if !opts.Force && normalizeVersion(opts.Current) == normalizeVersion(targetVersion) && !opts.InstallOnly {
		return result, nil
	}

	asset, err := assetName(targetVersion, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Result{}, err
	}
	baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", opts.Repo, targetVersion)
	tmpDir, err := os.MkdirTemp("", "token-burn-upgrade-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, asset)
	checksumPath := filepath.Join(tmpDir, "checksums.txt")
	if err := download(ctx, opts.HTTPClient, baseURL+"/"+asset, archivePath); err != nil {
		return Result{}, err
	}
	if err := download(ctx, opts.HTTPClient, baseURL+"/checksums.txt", checksumPath); err != nil {
		return Result{}, err
	}
	if err := verifyChecksum(archivePath, checksumPath, asset); err != nil {
		return Result{}, err
	}

	extracted, err := extractBinary(archivePath, tmpDir)
	if err != nil {
		return Result{}, err
	}
	if err := replaceBinary(extracted, opts.BinaryPath); err != nil {
		return Result{}, err
	}
	result.Changed = true
	return result, nil
}

func latestVersion(ctx context.Context, client *http.Client, repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create latest release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "token-burn")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("fetch latest release: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var release releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	if release.TagName == "" {
		return "", errors.New("latest release response did not include tag_name")
	}
	return release.TagName, nil
}

func assetName(version, goos, goarch string) (string, error) {
	switch goos {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("unsupported OS %q", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported architecture %q", goarch)
	}
	return fmt.Sprintf("token-burn_%s_%s_%s.tar.gz", version, goos, goarch), nil
}

func download(ctx context.Context, client *http.Client, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", "token-burn")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("download %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func verifyChecksum(archivePath, checksumPath, asset string) error {
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}
	expected := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == asset {
			expected = fields[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum for %s not found", asset)
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive for checksum: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch for %s", asset)
	}
	return nil
}

func extractBinary(archivePath, tmpDir string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "token-burn" {
			continue
		}
		out := filepath.Join(tmpDir, "token-burn.new")
		file, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return "", fmt.Errorf("create extracted binary: %w", err)
		}
		if _, err := io.Copy(file, tr); err != nil {
			file.Close()
			return "", fmt.Errorf("extract binary: %w", err)
		}
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("close extracted binary: %w", err)
		}
		if err := os.Chmod(out, 0755); err != nil {
			return "", fmt.Errorf("chmod extracted binary: %w", err)
		}
		return out, nil
	}
	return "", errors.New("archive did not contain token-burn binary")
}

func replaceBinary(source, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("create binary directory: %w", err)
	}
	info, err := os.Stat(dest)
	if err == nil {
		backup := dest + ".old"
		_ = os.Remove(backup)
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("backup existing binary: %w", err)
		}
		defer os.Remove(backup)
		if err := os.Chmod(source, info.Mode().Perm()); err != nil {
			return fmt.Errorf("preserve binary permissions: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat existing binary: %w", err)
	}
	if err := os.Rename(source, dest); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "dev" || value == "none" || value == "unknown" {
		return value
	}
	return strings.TrimPrefix(value, "v")
}
