package cli

import (
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/text/width"
)

type lineEdit struct {
	runes []rune
}

func (line *lineEdit) push(r rune) int {
	line.runes = append(line.runes, r)
	return runeWidth(r)
}

func (line *lineEdit) backspace() int {
	if len(line.runes) == 0 {
		return 0
	}
	last := line.runes[len(line.runes)-1]
	line.runes = line.runes[:len(line.runes)-1]
	return runeWidth(last)
}

func (line *lineEdit) clear() int {
	cols := 0
	for _, r := range line.runes {
		cols += runeWidth(r)
	}
	line.runes = nil
	return cols
}

func (line lineEdit) String() string {
	return string(line.runes)
}

func runeWidth(r rune) int {
	if r < 0x20 || r == 0x7f {
		return 0
	}
	kind := width.LookupRune(r).Kind()
	if kind == width.EastAsianWide || kind == width.EastAsianFullwidth {
		return 2
	}
	return 1
}

type terminalSession struct {
	file    *os.File
	old     *unix.Termios
	once    sync.Once
	mu      sync.Mutex
	handler func() bool
}

func openTerminal(in io.Reader) *terminalSession {
	file, ok := in.(*os.File)
	if !ok {
		return nil
	}
	fd := int(file.Fd())
	state, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil
	}
	next := *state
	next.Lflag &^= unix.ICANON | unix.ECHO
	next.Iflag |= unix.IUTF8
	next.Cc[unix.VMIN] = 1
	next.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &next); err != nil {
		return nil
	}
	session := &terminalSession{file: file, old: state}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for range sig {
			session.mu.Lock()
			handle := session.handler
			session.mu.Unlock()
			if handle != nil && handle() {
				continue
			}
			session.Close()
			signal.Stop(sig)
			signal.Reset(syscall.SIGINT, syscall.SIGTERM)
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
			return
		}
	}()
	return session
}

func (session *terminalSession) setHandler(handle func() bool) {
	if session == nil {
		return
	}
	session.mu.Lock()
	session.handler = handle
	session.mu.Unlock()
}

func (session *terminalSession) Close() {
	if session == nil {
		return
	}
	session.once.Do(func() {
		_ = unix.IoctlSetTermios(int(session.file.Fd()), unix.TCSETS, session.old)
	})
}

func (session *terminalSession) ReadLine(out io.Writer) (string, error) {
	var line lineEdit
	one := make([]byte, 1)
	for {
		if _, err := session.file.Read(one); err != nil {
			return "", err
		}
		b := one[0]
		switch b {
		case '\r', '\n':
			fmtErase(out, 0, true)
			return line.String(), nil
		case 0x7f, 0x08:
			fmtErase(out, line.backspace(), false)
		case 0x15:
			fmtErase(out, line.clear(), false)
		case 0x04:
			if len(line.runes) == 0 {
				fmtErase(out, 0, true)
				return "", io.EOF
			}
		case 0x1b:
			swallowEscape(session.file)
		default:
			if b < 0x20 {
				continue
			}
			raw := []byte{b}
			need := utf8LeadLen(b)
			for len(raw) < need {
				if _, err := session.file.Read(one); err != nil {
					return "", err
				}
				raw = append(raw, one[0])
			}
			r, _ := utf8.DecodeRune(raw)
			if r == utf8.RuneError && need > 1 {
				continue
			}
			line.push(r)
			_, _ = io.WriteString(out, string(r))
		}
	}
}

func fmtErase(out io.Writer, cols int, newline bool) {
	for i := 0; i < cols; i++ {
		_, _ = io.WriteString(out, "\b \b")
	}
	if newline {
		_, _ = io.WriteString(out, "\n")
	}
}

func utf8LeadLen(b byte) int {
	switch {
	case b&0x80 == 0:
		return 1
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 1
	}
}

func swallowEscape(file *os.File) {
	one := make([]byte, 1)
	if _, err := file.Read(one); err != nil {
		return
	}
	if one[0] != '[' && one[0] != 'O' {
		return
	}
	for {
		if _, err := file.Read(one); err != nil {
			return
		}
		if (one[0] >= 'A' && one[0] <= 'Z') || (one[0] >= 'a' && one[0] <= 'z') || one[0] == '~' {
			return
		}
	}
}
