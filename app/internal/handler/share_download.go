package handler

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// shareVisitorDownloads is how many files one visitor may download at once.
// A visitor over it waits up to shareSlotWait for a slot, so a page that
// loads a few files together still works.
const shareVisitorDownloads = 4

// After shareWriteGrace a download must average shareMinRate bytes per
// second, and no write may stall for longer than shareWriteIdle. A slow or
// stalled reader is cut off instead of holding a slot. This replaces
// PocketBase's flat 5-minute write timeout, which cut off large files on slow
// connections.
const shareMinRate = 32 << 10

// Variables so tests can shorten them.
var (
	shareSlotWait   = 10 * time.Second
	shareWriteGrace = time.Minute
	shareWriteIdle  = 2 * time.Minute
)

// shareDownloadWriter serves a shared file. It counts a download from the
// response actually sent, and paces the connection (see shareMinRate).
type shareDownloadWriter struct {
	http.ResponseWriter
	controller  *http.ResponseController
	method      string
	onDownload  func()
	started     time.Time
	sent        int64
	wroteHeader bool
}

func newShareDownloadWriter(w http.ResponseWriter, method string, onDownload func()) *shareDownloadWriter {
	return &shareDownloadWriter{ResponseWriter: w, controller: http.NewResponseController(w), method: method, onDownload: onDownload, started: time.Now()}
}

func (w *shareDownloadWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if w.onDownload != nil && deliversWholeFile(w.method, status, w.Header()) {
			w.onDownload()
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *shareDownloadWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	_ = w.controller.SetWriteDeadline(shareWriteDeadline(w.started, time.Now(), w.sent+int64(len(p))))
	n, err := w.ResponseWriter.Write(p)
	w.sent += int64(n)
	return n, err
}

// shareWriteDeadline is when the write ending at byte total must be done: on
// schedule for shareMinRate after the grace period, and within shareWriteIdle
// of now.
func shareWriteDeadline(started, now time.Time, total int64) time.Time {
	due := started.Add(shareWriteGrace + time.Duration(float64(total)/shareMinRate*float64(time.Second)))
	if idle := now.Add(shareWriteIdle); idle.Before(due) {
		return idle
	}
	return due
}

// Unwrap lets http.ResponseController reach the connection.
func (w *shareDownloadWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// deliversWholeFile reports whether a response hands over the file from its
// first byte: a full 200, a range starting at byte 0, or several ranges at
// once. Follow-up ranges, such as a PDF viewer's, are not downloads. The
// decision uses what the server sends, so no Range header trick avoids it.
func deliversWholeFile(method string, status int, header http.Header) bool {
	if method != http.MethodGet {
		return false
	}
	switch status {
	case http.StatusOK:
		return true
	case http.StatusPartialContent:
		return strings.HasPrefix(header.Get("Content-Range"), "bytes 0-") ||
			strings.HasPrefix(header.Get("Content-Type"), "multipart/byteranges")
	}
	return false
}

// shareSlots limits concurrent downloads per visitor.
type shareSlots struct {
	mu       sync.Mutex
	max      int
	visitors map[string]*shareSlot
}

type shareSlot struct {
	held  chan struct{}
	users int
}

func newShareSlots(max int) *shareSlots {
	return &shareSlots{max: max, visitors: make(map[string]*shareSlot)}
}

// acquire takes one of key's slots, waiting up to wait. When ok, release must
// be called once the download ends.
func (s *shareSlots) acquire(ctx context.Context, key string, wait time.Duration) (release func(), ok bool) {
	s.mu.Lock()
	slot := s.visitors[key]
	if slot == nil {
		slot = &shareSlot{held: make(chan struct{}, s.max)}
		s.visitors[key] = slot
	}
	slot.users++
	s.mu.Unlock()

	leave := func(acquired bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if acquired {
			<-slot.held
		}
		slot.users--
		if slot.users == 0 {
			delete(s.visitors, key)
		}
	}
	release = sync.OnceFunc(func() { leave(true) })
	select {
	case slot.held <- struct{}{}:
		return release, true
	default:
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case slot.held <- struct{}{}:
		return release, true
	case <-timer.C:
	case <-ctx.Done():
	}
	leave(false)
	return nil, false
}
