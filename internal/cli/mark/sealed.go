package mark

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

// Sealed frames: the halves start this many columns apart and slide together.
const (
	sealedFrames = 6
	sealedTotal  = 300 * time.Millisecond
	sealedStep   = sealedTotal / (sealedFrames - 1)
	sealedShift  = sealedFrames - 1 // columns each half starts away from home
)

// ErrInterrupted is returned when Ctrl-C stops the animation (the cursor is restored first).
var ErrInterrupted = errors.New("mark: interrupted")

// Sealed draws the closing "Sealed" moment (brand §10, §13.3): with o.Anim, the two halves
// slide together over 300 ms in 6 frames, with the cursor hidden and restored (on Ctrl-C too);
// otherwise only the final frame. It always ends with the ✓ Sealed line; when o.Quiet that line
// is all it writes, with no escape codes.
func Sealed(w io.Writer, o Options) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	defer signal.Stop(stop)
	return sealed(w, o, time.Sleep, stop)
}

func sealed(w io.Writer, o Options, sleep func(time.Duration), stop <-chan os.Signal) error {
	if o.Quiet || o.Depth == DepthASCII {
		_, err := io.WriteString(w, "✓ "+SealedLine+"\n")
		return err
	}
	g := grid(o.Theme)
	frame := func(shift int) string {
		var b strings.Builder
		b.Grow(10 << 10)
		pad := strings.Repeat(" ", sealedShift-shift)
		for row := 0; row < orbRows; row++ {
			b.WriteString("\x1b[2K")
			b.WriteString(pad)
			writeOrbRow(&b, o, g, row, shift, false)
			b.WriteByte('\n')
		}
		return b.String()
	}
	if o.Anim {
		if _, err := io.WriteString(w, "\x1b[?25l"); err != nil {
			return err
		}
		restore := func() { io.WriteString(w, "\x1b[0m\x1b[?25h") }
		for i := 0; i < sealedFrames-1; i++ {
			if _, err := io.WriteString(w, frame(sealedShift-i)); err != nil {
				restore()
				return err
			}
			sleep(sealedStep)
			select {
			case <-stop:
				restore()
				return ErrInterrupted
			default:
			}
			io.WriteString(w, "\x1b["+itoa(orbRows)+"A\r")
		}
		defer restore()
	}
	var b strings.Builder
	b.WriteString(frame(0))
	b.WriteByte('\n')
	ok := "✓"
	if o.Depth >= Depth16 {
		c, c16 := cverified, 6
		if o.Theme == ThemeLight {
			c = cverifiedLight
		}
		sgrFG(&b, o, c, c16)
		ok = "✓\x1b[0m"
	}
	b.WriteString(ok + " " + SealedLine + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func itoa(n int) string {
	var buf [8]byte
	i := len(buf)
	for {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(buf[i:])
		}
	}
}
