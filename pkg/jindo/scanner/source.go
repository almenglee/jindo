package scanner

import (
	"io"
	"unicode/utf8"
)

type source struct {
	in   io.Reader
	errh func(line, col uint, msg string)

	buf       []byte // source buffer
	ioerr     error  // pending I/O error, or nil
	b, r, e   int    // buffer indices (see comment above)
	line, col uint   // source position of ch (0-based)
	ch        rune   // most recently read character
	chw       int    // width of ch
}

const sentinel = utf8.RuneSelf

func (s *source) init(in io.Reader, errh func(line, col uint, msg string)) {
	s.in = in
	s.errh = errh

	if s.buf == nil {
		s.buf = make([]byte, nextSize(0))
	}
	s.buf[0] = sentinel
	s.ioerr = nil
	s.b, s.r, s.e = -1, 0, 0
	s.line, s.col = 0, 0
	s.ch = ' '
	s.chw = 0
}

// starting points for Line and column numbers
const Linebase = 1
const Colbase = 1

// pos returns the (line, col) source position of s.ch.
func (s *source) pos() (line, col uint) {
	return Linebase + s.line, Colbase + s.col
}

// error reports the error msg at source position s.pos().
func (s *source) error(msg string) {
	line, col := s.pos()
	s.errh(line, col, msg)
}

// start starts a new active source Segment (including s.ch).
// As long as stop has not been called, the active Segment's
// bytes (excluding s.ch) may be retrieved by calling Segment.
func (s *source) start()          { s.b = s.r - s.chw }
func (s *source) stop()           { s.b = -1 }
func (s *source) Segment() []byte { return s.buf[s.b : s.r-s.chw] }

// rewind rewinds the scanner's read position and character s.ch
// to the start of the currently active Segment, which must not
// contain any newlines (otherwise position information will be
// incorrect). Currently, rewind is only needed for handling the
// source sequence ".."; it must not be called outside an active
// Segment.
func (s *source) rewind() {
	// ok to verify precondition - rewind is rarely called
	if s.b < 0 {
		panic("no active Segment")
	}
	s.col -= uint(s.r - s.b)
	s.r = s.b
	s.nextch()
}

func (s *source) nextch() {
redo:
	s.col += uint(s.chw)
	if s.ch == '\n' {
		s.line++
		s.col = 0
	}

	// fast common case: at least one ASCII character
	if s.ch = rune(s.buf[s.r]); s.ch < sentinel {
		s.r++
		s.chw = 1
		if s.ch == 0 {
			s.error("invalid NUL character")
			goto redo
		}
		return
	}

	// slower general case: add more bytes to buffer if we don't have a full rune
	for s.e-s.r < utf8.UTFMax && !utf8.FullRune(s.buf[s.r:s.e]) && s.ioerr == nil {
		s.fill()
	}

	// EOF
	if s.r == s.e {
		if s.ioerr != io.EOF {
			// ensure we never start with a '/' (e.g., rooted path) in the error message
			s.error("I/O error: " + s.ioerr.Error())
			s.ioerr = nil
		}
		s.ch = -1
		s.chw = 0
		return
	}

	s.ch, s.chw = utf8.DecodeRune(s.buf[s.r:s.e])
	s.r += s.chw

	if s.ch == utf8.RuneError && s.chw == 1 {
		s.error("invalid UTF-8 encoding")
		goto redo
	}

	// BOM's are only allowed as the first character in a file
	const BOM = 0xfeff
	if s.ch == BOM {
		if s.line > 0 || s.col > 0 {
			s.error("invalid BOM in the middle of the file")
		}
		goto redo
	}
}

// fill reads more source bytes into s.buf.
// It returns with at least one more byte in the buffer, or with s.ioerr != nil.
func (s *source) fill() {
	// determine content to preserve
	b := s.r
	if s.b >= 0 {
		b = s.b
		s.b = 0 // after buffer has grown or content has been moved down
	}
	content := s.buf[b:s.e]

	// grow buffer or move content down
	if len(content)*2 > len(s.buf) {
		s.buf = make([]byte, nextSize(len(s.buf)))
		copy(s.buf, content)
	} else if b > 0 {
		copy(s.buf, content)
	}
	s.r -= b
	s.e -= b

	// read more data: try a limited number of times
	for i := 0; i < 10; i++ {
		var n int
		n, s.ioerr = s.in.Read(s.buf[s.e : len(s.buf)-1]) // -1 to leave space for sentinel
		if n < 0 {
			panic("negative read") // incorrect underlying io.Reader implementation
		}
		if n > 0 || s.ioerr != nil {
			s.e += n
			s.buf[s.e] = sentinel
			return
		}
		// n == 0
	}

	s.buf[s.e] = sentinel
	s.ioerr = io.ErrNoProgress
}

// nextSize returns the Next bigger size for a buffer of a given size.
func nextSize(size int) int {
	const smin = 4 << 10 // 4K: minimum buffer size
	const smax = 1 << 20 // 1M: maximum buffer size which is still doubled
	if size < smin {
		return smin
	}
	if size <= smax {
		return size << 1
	}
	return size + smax
}