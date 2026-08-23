package httpx

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
)

// StatusWriter captures the status code of a response while streaming it
// through unchanged. Used for request logging.
type StatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *StatusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *StatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *StatusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Status reports the captured status code, defaulting to 200.
func (w *StatusWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// Recorder is a StatusWriter that additionally copies the body into a
// buffer so middleware (response caching) can store a snapshot. The
// response still streams through to the client unmodified.
type Recorder struct {
	StatusWriter
	body bytes.Buffer
}

func NewRecorder(w http.ResponseWriter) *Recorder {
	return &Recorder{StatusWriter: StatusWriter{ResponseWriter: w}}
}

func (w *Recorder) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// Snapshot returns the captured status, headers and body.
func (w *Recorder) Snapshot() (int, http.Header, []byte) {
	return w.Status(), w.Header(), w.body.Bytes()
}
