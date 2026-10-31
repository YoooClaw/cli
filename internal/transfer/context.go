package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if r.ctx != nil {
		if e := r.ctx.Err(); e != nil {
			return 0, e
		}
	}
	return r.reader.Read(b)
}
func hashFileContext(ctx context.Context, p string) (string, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, contextReader{ctx, f})
	return hex.EncodeToString(h.Sum(nil)), e
}
