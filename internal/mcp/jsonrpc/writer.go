package jsonrpc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"sync"
)

// ErrClosed is Send after Close.
var ErrClosed = errors.New("jsonrpc: writer closed")

// Writer owns one stream: a single goroutine writes, so messages never interleave, and each
// message is exactly one line. Send never blocks on the stream.
type Writer struct {
	mu      sync.Mutex
	queue   [][]byte
	wake    chan struct{}
	closed  bool
	done    chan struct{}
	err     error
	w       *bufio.Writer
	flushFn func() error
}

// NewWriter starts the writing goroutine on w.
func NewWriter(w io.Writer) *Writer {
	bw := bufio.NewWriterSize(w, 64<<10)
	x := &Writer{wake: make(chan struct{}, 1), done: make(chan struct{}), w: bw, flushFn: bw.Flush}
	go x.loop()
	return x
}

// Send queues one message. A message that holds a newline (only possible from a caller bug, since
// Envelope.Marshal writes compact members) is refused rather than split.
func (x *Writer) Send(msg []byte) error {
	if bytes.IndexByte(msg, '\n') >= 0 {
		return errors.New("jsonrpc: a message must not contain a newline")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return ErrClosed
	}
	if x.err != nil {
		return x.err
	}
	x.queue = append(x.queue, msg)
	select {
	case x.wake <- struct{}{}:
	default:
	}
	return nil
}

// SendEnvelope is Send(e.Marshal()).
func (x *Writer) SendEnvelope(e Envelope) error { return x.Send(e.Marshal()) }

func (x *Writer) loop() {
	defer close(x.done)
	for {
		x.mu.Lock()
		q := x.queue
		x.queue = nil
		closed := x.closed
		x.mu.Unlock()
		for _, m := range q {
			if _, err := x.w.Write(m); err != nil {
				x.fail(err)
				return
			}
			if err := x.w.WriteByte('\n'); err != nil {
				x.fail(err)
				return
			}
		}
		if len(q) > 0 {
			if err := x.flushFn(); err != nil {
				x.fail(err)
				return
			}
		}
		if closed && len(q) == 0 {
			return
		}
		if len(q) == 0 {
			<-x.wake
		}
	}
}

func (x *Writer) fail(err error) {
	x.mu.Lock()
	x.err = err
	x.queue = nil
	x.mu.Unlock()
}

// Close writes what is queued, then stops. It returns the first write error.
func (x *Writer) Close() error {
	x.mu.Lock()
	if !x.closed {
		x.closed = true
		select {
		case x.wake <- struct{}{}:
		default:
		}
	}
	x.mu.Unlock()
	<-x.done
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.err
}

// Err is the write error, if the stream broke.
func (x *Writer) Err() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.err
}
