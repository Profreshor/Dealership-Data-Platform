package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type cancelArchiveWriter struct {
	io.Writer
	cancel context.CancelFunc
}

func (w cancelArchiveWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.cancel()
	return n, err
}

func TestArchiveCopyStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	data := bytes.Repeat([]byte("synthetic archive"), 65536)
	var output bytes.Buffer
	n, err := io.Copy(cancelArchiveWriter{&output, cancel}, archiveReader{ctx, bytes.NewReader(data)})
	if !errors.Is(err, context.Canceled) || n == 0 || n >= int64(len(data)) || n != int64(output.Len()) {
		t.Fatalf("archive copy did not stop between chunks: bytes=%d, error=%v", n, err)
	}
}
