package assets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
)

var errBudget = errors.New("scan budget reached")

type lineReader struct {
	ctx       context.Context
	f         *os.File
	end       int64
	remaining *int
	base      int64
	block     []byte
}
type line struct {
	raw             []byte
	start, end      int64
	recordEnd       int64
	oversized, tail bool
}

// Reads are aligned and cached; scanning a thousand short rows does not perform
// a thousand disk reads. Never read appended bytes beyond the captured EOF.
func (r *lineReader) at(pos int64) ([]byte, int64, error) {
	if e := r.ctx.Err(); e != nil {
		return nil, 0, e
	}
	base := pos / int64(BlockSize) * int64(BlockSize)
	if len(r.block) > 0 && base == r.base {
		return r.block, base, nil
	}
	n := int(min(int64(BlockSize), r.end-base))
	if n > *r.remaining {
		return nil, 0, errBudget
	}
	buf := make([]byte, n)
	read, e := r.f.ReadAt(buf, base)
	*r.remaining -= read
	if e != nil && e != io.EOF {
		return nil, 0, e
	}
	if read != n {
		return nil, 0, io.ErrUnexpectedEOF
	}
	r.base = base
	r.block = buf
	return buf, base, nil
}

// next only commits complete records. A budget stop in a normal line rewinds
// that candidate; an oversized line records a bounded skip state with no text.
func (r *lineReader) next(c cursor) (line, cursor, error) {
	startCursor := c
	pos := c.position()
	anchor := pos
	oversized := c.SkipLine != nil
	var raw []byte
	var scanned int64
	endHasLF := false
	if oversized {
		anchor = c.SkipLine.Anchor
		scanned = c.SkipLine.Scanned
		endHasLF = c.SkipLine.EndHasLF
	}
	tail := false
	if !c.forward() && !oversized && pos > 0 {
		block, base, e := r.at(pos - 1)
		if e != nil {
			return line{}, c, e
		}
		if block[pos-1-base] == '\n' {
			endHasLF = true
			pos--
		} else {
			tail = pos == r.end
		}
	}
	finish := func() (line, cursor, error) {
		c.setPosition(pos)
		c.SkipLine = nil
		row := line{raw: bytes.TrimSuffix(raw, []byte{'\r'}), oversized: oversized, tail: tail}
		if c.forward() {
			row.start = anchor
			row.end = pos
		} else {
			row.start = pos
			row.end = anchor
		}
		// Exclude the separator from identity: adding a missing EOF newline
		// during a subsequent append must not change the previous record ID.
		row.recordEnd = row.end
		if endHasLF {
			row.recordEnd--
		}
		return row, c, nil
	}
	for {
		if c.forward() && pos >= r.end {
			tail = true
			return finish()
		}
		if !c.forward() && pos <= 0 {
			return finish()
		}
		at := pos
		if !c.forward() {
			at--
		}
		block, base, e := r.at(at)
		if e != nil {
			if errors.Is(e, errBudget) && oversized {
				c.setPosition(pos)
				c.SkipLine = &skipLine{Anchor: anchor, Scanned: scanned, EndHasLF: endHasLF}
				return line{}, c, e
			}
			return line{}, startCursor, e
		}
		var part []byte
		found := false
		if c.forward() {
			part = block[pos-base:]
			if i := bytes.IndexByte(part, '\n'); i >= 0 {
				part = part[:i]
				found = true
			}
			pos += int64(len(part))
			if found {
				endHasLF = true
				pos++
			}
		} else {
			part = block[:pos-base]
			if i := bytes.LastIndexByte(part, '\n'); i >= 0 {
				part = part[i+1:]
				found = true
			}
			pos -= int64(len(part))
		}
		scanned += int64(len(part))
		if scanned > LineLimit {
			oversized = true
			raw = nil
		}
		if !oversized {
			if c.forward() {
				raw = append(raw, part...)
			} else {
				joined := make([]byte, len(part)+len(raw))
				copy(joined, part)
				copy(joined[len(part):], raw)
				raw = joined
			}
		}
		if found {
			return finish()
		}
	}
}
