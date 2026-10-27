package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const maxRolloutBytes int64 = 512 << 20
const maxZstdWindow = 64 << 20

var errRolloutTooLarge = errors.New("decoded rollout exceeds 512 MiB limit")

type rolloutReader struct {
	file      *os.File
	decoder   *zstd.Decoder
	reader    io.Reader
	remaining int64
	Path      string
}

func isRolloutPath(path string) bool {
	return strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".jsonl.zst")
}

// Opening an old database .jsonl path may resolve its compressed sibling.
// Never extract to disk or open a Codex source with write access.
func openRollout(path string) (*rolloutReader, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && strings.HasSuffix(path, ".jsonl") {
		path += ".zst"
		file, err = os.Open(path)
	}
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("rollout is not a regular file")
	}
	r := &rolloutReader{file: file, reader: file, remaining: maxRolloutBytes, Path: path}
	if strings.HasSuffix(path, ".jsonl.zst") {
		r.decoder, err = zstd.NewReader(file, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxMemory(maxZstdWindow), zstd.WithDecoderMaxWindow(maxZstdWindow))
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("initialize zstd rollout decoder: %w", err)
		}
		r.reader = r.decoder
	}
	return r, nil
}

func (r *rolloutReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			return 0, errRolloutTooLarge
		}
		return 0, r.readError(err)
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, r.readError(err)
}

func (r *rolloutReader) readError(err error) error {
	if err != nil && err != io.EOF && r.decoder != nil {
		return fmt.Errorf("decode zstd rollout: %w", err)
	}
	return err
}

func (r *rolloutReader) Close() error {
	if r.decoder != nil {
		r.decoder.Close()
	}
	return r.file.Close()
}

func newRolloutScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), scannerBufferSize)
	return scanner
}

func rolloutScanError(scanner *bufio.Scanner) error {
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return errors.New("rollout JSONL record exceeds 64 MiB limit")
	}
	return scanner.Err()
}

// The uncompressed path is the logical source identity, preserving V1 indexes
// across Codex's storage-format changes. Content is not part of this key.
func logicalRolloutPath(path string) string {
	path = normalizePath(path)
	if strings.HasSuffix(path, ".jsonl.zst") {
		return strings.TrimSuffix(path, ".zst")
	}
	return path
}
