package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	compose "github.com/compose-spec/compose-go/v2/types"
	"github.com/defang-io/defang/src/pkg/cli/client"
	"github.com/defang-io/defang/src/pkg/http"
	"github.com/defang-io/defang/src/pkg/term"
	defangv1 "github.com/defang-io/defang/src/protos/io/defang/v1"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
	"github.com/sirupsen/logrus"
)

const (
	MiB                 = 1024 * 1024
	ContextFileLimit    = 10
	ContextSizeLimit    = 10 * MiB
	sourceDateEpoch     = 315532800 // 1980-01-01, same as nix-shell
	defaultDockerIgnore = `# Default .dockerignore file for Defang
**/.DS_Store
**/.direnv
**/.envrc
**/.git
**/.github
**/.idea
**/.next
**/.vscode
**/__pycache__
**/compose.yaml
**/compose.yml
**/defang.exe
**/docker-compose.yml
**/docker-compose.yaml
**/node_modules
**/Thumbs.db
# Ignore our own binary, but only in the root to avoid ignoring subfolders
defang`
)

var (
	nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]+`)
)

type ComposeError struct {
	error
}

func (e ComposeError) Unwrap() error {
	return e.error
}

func NormalizeServiceName(s string) string {
	return nonAlphanumeric.ReplaceAllLiteralString(strings.ToLower(s), "-")
}

func warnf(format string, args ...interface{}) {
	logrus.Warnf(format, args...)
	term.HadWarnings = true
}

func getRemoteBuildContext(ctx context.Context, client client.Client, name string, build *compose.BuildConfig, force bool) (string, error) {
	root, err := filepath.Abs(build.Context)
	if err != nil {
		return "", fmt.Errorf("invalid build context: %w", err)
	}

	term.Info(" * Compressing build context for", name, "at", root)
	buffer, err := createTarball(ctx, build.Context, build.Dockerfile)
	if err != nil {
		return "", err
	}

	var digest string
	if !force {
		// Calculate the digest of the tarball and pass it to the fabric controller (to avoid building the same image twice)
		sha := sha256.Sum256(buffer.Bytes())
		digest = "sha256-" + base64.StdEncoding.EncodeToString(sha[:]) // same as Nix
		term.Debug(" - Digest:", digest)
	}

	if DoDryRun {
		return root, nil
	}

	term.Info(" * Uploading build context for", name)
	return uploadTarball(ctx, client, buffer, digest)
}

// We can changed to slices.contains when we upgrade to go 1.21 or above
var validProtocols = map[string]bool{"": true, "tcp": true, "udp": true, "http": true, "http2": true, "grpc": true}
var validModes = map[string]bool{"": true, "host": true, "ingress": true}

func validatePort(port compose.ServicePortConfig) error {
	if port.Target < 1 || port.Target > 32767 {
		return fmt.Errorf("port 'target' must be an integer between 1 and 32767: %v", port.Target)
	}
	if port.HostIP != "" {
		return errors.New("port 'host_ip' is not supported")
	}
	if !validProtocols[port.Protocol] {
		return fmt.Errorf("port 'protocol' not one of [tcp udp http http2 grpc]: %v", port.Protocol)
	}
	if !validModes[port.Mode] {
		return fmt.Errorf("port 'mode' not one of [host ingress]: %v", port.Mode)
	}
	if port.Published != "" && (port.Mode == "host" || port.Protocol == "udp") {
		portRange := strings.SplitN(port.Published, "-", 2)
		start, err := strconv.ParseUint(portRange[0], 10, 16)
		if err != nil {
			return fmt.Errorf("port 'published' start must be an integer: %v", portRange[0])
		}
		if len(portRange) == 2 {
			end, err := strconv.ParseUint(portRange[1], 10, 16)
			if err != nil {
				return fmt.Errorf("port 'published' end must be an integer: %v", portRange[1])
			}
			if start > end {
				return fmt.Errorf("port 'published' start must be less than end: %v", port.Published)
			}
			if port.Target < uint32(start) || port.Target > uint32(end) {
				return fmt.Errorf("port 'published' range must include 'target': %v", port.Published)
			}
		} else {
			if start != uint64(port.Target) {
				return fmt.Errorf("port 'published' must be empty or equal to 'target': %v", port.Published)
			}
		}
	}

	return nil
}

func validatePorts(ports []compose.ServicePortConfig) error {
	for _, port := range ports {
		err := validatePort(port)
		if err != nil {
			return err
		}
	}
	return nil
}

func convertPort(port compose.ServicePortConfig) *defangv1.Port {
	pbPort := &defangv1.Port{
		// Mode      string `yaml:",omitempty" json:"mode,omitempty"`
		// HostIP    string `mapstructure:"host_ip" yaml:"host_ip,omitempty" json:"host_ip,omitempty"`
		// Published string `yaml:",omitempty" json:"published,omitempty"`
		// Protocol  string `yaml:",omitempty" json:"protocol,omitempty"`
		Target: port.Target,
	}

	switch port.Protocol {
	case "":
		pbPort.Protocol = defangv1.Protocol_ANY // defaults to HTTP in CD
	case "tcp":
		pbPort.Protocol = defangv1.Protocol_TCP
	case "udp":
		pbPort.Protocol = defangv1.Protocol_UDP
	case "http": // TODO: not per spec
		pbPort.Protocol = defangv1.Protocol_HTTP
	case "http2": // TODO: not per spec
		pbPort.Protocol = defangv1.Protocol_HTTP2
	case "grpc": // TODO: not per spec
		pbPort.Protocol = defangv1.Protocol_GRPC
	default:
		panic(fmt.Sprintf("port 'protocol' should have been validated to be one of [tcp udp http http2 grpc] but got: %v", port.Protocol))
	}

	switch port.Mode {
	case "":
		warnf("No port 'mode' was specified; defaulting to 'ingress' (add 'mode: ingress' to silence)")
		fallthrough
	case "ingress":
		// This code is unnecessarily complex because compose-go silently converts short port: syntax to ingress+tcp
		if port.Protocol != "udp" {
			if port.Published != "" {
				warnf("Published ports are ignored in ingress mode")
			}
			pbPort.Mode = defangv1.Mode_INGRESS
			if pbPort.Protocol == defangv1.Protocol_TCP || pbPort.Protocol == defangv1.Protocol_UDP {
				warnf("TCP ingress is not supported; assuming HTTP (remove 'protocol' to silence)")
				pbPort.Protocol = defangv1.Protocol_HTTP
			}
			break
		}
		warnf("UDP ports default to 'host' mode (add 'mode: host' to silence)")
		fallthrough
	case "host":
		pbPort.Mode = defangv1.Mode_HOST
	default:
		panic(fmt.Sprintf("port mode should have been validated to be one of [host ingress] but got: %v", port.Mode))
	}
	return pbPort
}

func convertPorts(ports []compose.ServicePortConfig) []*defangv1.Port {
	var pbports []*defangv1.Port
	for _, port := range ports {
		pbPort := convertPort(port)
		pbports = append(pbports, pbPort)
	}
	return pbports
}

func uploadTarball(ctx context.Context, client client.Client, body io.Reader, digest string) (string, error) {
	// Upload the tarball to the fabric controller storage;; TODO: use a streaming API
	ureq := &defangv1.UploadURLRequest{Digest: digest}
	res, err := client.CreateUploadURL(ctx, ureq)
	if err != nil {
		return "", err
	}

	// Do an HTTP PUT to the generated URL
	resp, err := http.Put(ctx, res.Url, "application/gzip", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP PUT failed with status code %v", resp.Status)
	}

	return http.RemoveQueryParam(res.Url), nil
}

type contextAwareWriter struct {
	ctx context.Context
	io.WriteCloser
}

func (cw contextAwareWriter) Write(p []byte) (n int, err error) {
	select {
	case <-cw.ctx.Done(): // Detect context cancelation
		return 0, cw.ctx.Err()
	default:
		return cw.WriteCloser.Write(p)
	}
}

func tryReadIgnoreFile(cwd, ignorefile string) io.ReadCloser {
	path := filepath.Join(cwd, ignorefile)
	reader, err := os.Open(path)
	if err != nil {
		return nil
	}
	term.Debug(" - Reading .dockerignore file from", ignorefile)
	return reader
}

func createTarball(ctx context.Context, root, dockerfile string) (*bytes.Buffer, error) {
	foundDockerfile := false
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	} else {
		dockerfile = filepath.Clean(dockerfile)
	}

	// A Dockerfile-specific ignore-file takes precedence over the .dockerignore file at the root of the build context if both exist.
	dockerignore := dockerfile + ".dockerignore"
	reader := tryReadIgnoreFile(root, dockerignore)
	if reader == nil {
		dockerignore = ".dockerignore"
		reader = tryReadIgnoreFile(root, dockerignore)
		if reader == nil {
			term.Debug(" - No .dockerignore file found; using defaults")
			reader = io.NopCloser(strings.NewReader(defaultDockerIgnore))
		}
	}
	patterns, err := ignorefile.ReadAll(reader) // handles comments and empty lines
	reader.Close()
	if err != nil {
		return nil, err
	}
	pm, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, err
	}

	// TODO: use io.Pipe and do proper streaming (instead of buffering everything in memory)
	fileCount := 0
	var buf bytes.Buffer
	gzipWriter := &contextAwareWriter{ctx, gzip.NewWriter(&buf)}
	tarWriter := tar.NewWriter(gzipWriter)

	doProgress := term.DoColor(term.Stdout) && term.IsTerminal
	err = filepath.WalkDir(root, func(path string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Don't include the root directory itself in the tarball
		if path == root {
			return nil
		}

		// Make sure the path is relative to the root
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		baseName := filepath.ToSlash(relPath)

		// we need the Dockerfile, even if it's in the .dockerignore file
		if !foundDockerfile && relPath == dockerfile {
			foundDockerfile = true
		} else if relPath == dockerignore {
			// we need the .dockerignore file too: it might ignore itself and/or the Dockerfile
		} else {
			// Ignore files using the dockerignore patternmatcher
			ignore, err := pm.MatchesOrParentMatches(baseName)
			if err != nil {
				return err
			}
			if ignore {
				term.Debug(" - Ignoring", relPath)
				if de.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		if term.DoDebug {
			term.Debug(" - Adding", baseName)
		} else if doProgress {
			fmt.Printf("%4d %s\r", fileCount, baseName)
			defer term.Stdout.ClearLine()
		}

		info, err := de.Info()
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		// Make reproducible; WalkDir walks files in lexical order.
		header.ModTime = time.Unix(sourceDateEpoch, 0)
		header.Gid = 0
		header.Uid = 0
		header.Name = baseName
		err = tarWriter.WriteHeader(header)
		if err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		fileCount++
		if fileCount == ContextFileLimit+1 {
			term.Warnf(" ! The build context contains more than %d files; use --debug or create .dockerignore", ContextFileLimit)
		}

		_, err = io.Copy(tarWriter, file)
		if buf.Len() > ContextSizeLimit {
			return fmt.Errorf("build context is too large; this beta version is limited to %dMiB", ContextSizeLimit/MiB)
		}
		return err
	})

	if err != nil {
		return nil, err
	}

	// Close the tar and gzip writers before returning the buffer
	if err = tarWriter.Close(); err != nil {
		return nil, err
	}

	if err = gzipWriter.Close(); err != nil {
		return nil, err
	}

	if !foundDockerfile {
		return nil, fmt.Errorf("the specified dockerfile could not be read: %q", dockerfile)
	}

	return &buf, nil
}
