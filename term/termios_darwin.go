package term

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// IOCTL terminal stuff.
const (
	TCGETS       = 0x5401     // TCGETS get terminal attributes
	TCSETS       = 0x5402     // TCSETS set terminal attributes
	TIOCGWINSZ   = 0x5413     // TIOCGWINSZ used to get the terminal window size
	TIOCSWINSZ   = 0x5414     // TIOCSWINSZ used to set the terminal window size
	TIOCGPTN     = 0x80045430 // TIOCGPTN IOCTL used to get the PTY number
	TIOCSPTLCK   = 0x40045431 // TIOCSPTLCK IOCT used to lock/unlock PTY
	CBAUD        = 0o010017   // CBAUD Serial speed settings
	CBAUDEX      = 0o010000   // CBAUDX Serial speed settings
	TIOCPTMASTER = 0x2000741c
	// FreeBSD posix_openpt syscall.
	OPENPT = 504
)

// from <sys/ioccom.h>
const (
	_IOC_VOID    uintptr = 0x20000000
	_IOC_OUT     uintptr = 0x40000000
	_IOC_IN      uintptr = 0x80000000
	_IOC_IN_OUT  uintptr = _IOC_OUT | _IOC_IN
	_IOC_DIRMASK         = _IOC_VOID | _IOC_OUT | _IOC_IN

	_IOC_PARAM_SHIFT = 13
	_IOC_PARAM_MASK  = (1 << _IOC_PARAM_SHIFT) - 1
)

func ioctl(fd, cmd, ptr uintptr) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, cmd, ptr)
	if e != 0 {
		return e
	}
	return nil
}

func _IOC_PARM_LEN(ioctl uintptr) uintptr {
	return (ioctl >> 16) & _IOC_PARAM_MASK
}

func _IOC(inout uintptr, group byte, ioctl_num uintptr, param_len uintptr) uintptr {
	return inout | (param_len&_IOC_PARAM_MASK)<<16 | uintptr(group)<<8 | ioctl_num
}

func _IO(group byte, ioctl_num uintptr) uintptr {
	return _IOC(_IOC_VOID, group, ioctl_num, 0)
}

// Set Sets terminal t attributes on file.
func (t *Termios) Set(file *os.File) error {
	fd := file.Fd()
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(TCSETS), uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}

// Attr Gets (terminal related) attributes from file.
func Attr(file *os.File) (Termios, error) {
	var t Termios
	fd := file.Fd()
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(TCGETS), uintptr(unsafe.Pointer(&t)))
	if errno != 0 {
		return t, errno
	}
	t.Ispeed &= CBAUD | CBAUDEX
	t.Ospeed &= CBAUD | CBAUDEX
	return t, nil
}

// Isatty returns true if file is a tty.
func Isatty(file *os.File) bool {
	_, err := Attr(file)
	return err == nil
}

// GetPass reads password from a TTY with no echo.
func GetPass(prompt string, f *os.File, pbuf []byte) ([]byte, error) {
	t, err := Attr(f)
	if err != nil {
		return nil, err
	}
	defer t.Set(f)
	noecho := t
	noecho.Lflag = noecho.Lflag &^ ECHO
	if err := noecho.Set(f); err != nil {
		return nil, err
	}
	b := make([]byte, 1, 1)
	i := 0
	if _, err := f.Write([]byte(prompt)); err != nil {
		return nil, err
	}
	for ; i < len(pbuf); i++ {
		if _, err := f.Read(b); err != nil {
			b[0] = 0
			clearbuf(pbuf[:i+1])
		}
		if b[0] == '\n' || b[0] == '\r' {
			return pbuf[:i], nil
		}
		pbuf[i] = b[0]
		b[0] = 0
	}
	clearbuf(pbuf[:i+1])
	return nil, errors.New("ran out of bufferspace")
}

// clearbuf clears out the buffer incase we couldn't read the full password.
func clearbuf(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Winsz Fetches the current terminal windowsize.
// example handling changing window sizes with PTYs:
//
// import "os"
// import "os/signal"
//
// var sig = make(chan os.Signal,2) 		// Channel to listen for UNIX SIGNALS on
// signal.Notify(sig, syscall.SIGWINCH) // That'd be the window changing
//
//	for {
//		<-sig
//		term.Winsz(os.Stdin)			// We got signaled our terminal changed size so we read in the new value
//	 term.Setwinsz(pty.Slave) // Copy it to our virtual Terminal
//	}
func (t *Termios) Winsz(file *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(file.Fd()), uintptr(TIOCGWINSZ), uintptr(unsafe.Pointer(&t.Wz)))
	if errno != 0 {
		return errno
	}
	return nil
}

// Setwinsz Sets the terminal window size.
func (t *Termios) Setwinsz(file *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(file.Fd()), uintptr(TIOCSWINSZ), uintptr(unsafe.Pointer(&t.Wz)))
	if errno != 0 {
		return errno
	}
	return nil
}

type dname struct {
	len int
	buf unsafe.Pointer
}

const (
	pathDev = "/dev/ptmx"
)

// OpenPTY Creates a new Master/Slave PTY pair.
func OpenPTY() (*PTY, error) {
	p, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	sname, err := ptsname(p)
	if err != nil {
		return nil, err
	}

	err = grantpt(p)
	if err != nil {
		return nil, err
	}

	err = unlockpt(p)
	if err != nil {
		return nil, err
	}

	t, err := os.OpenFile(sname, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	return &PTY{Master: p, Slave: t}, nil
}

func grantpt(f *os.File) error {
	return ioctl(f.Fd(), syscall.TIOCPTYGRANT, 0)
}

func unlockpt(f *os.File) error {
	return ioctl(f.Fd(), syscall.TIOCPTYUNLK, 0)
}

type winsize struct {
	ws_row    uint16
	ws_col    uint16
	ws_xpixel uint16
	ws_ypixel uint16
}

func setsize(f *os.File, rows uint16, cols uint16) error {
	var ws winsize
	ws.ws_row = rows
	ws.ws_col = cols
	return ioctl(f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

func ptsname(f *os.File) (string, error) {
	n := make([]byte, _IOC_PARM_LEN(syscall.TIOCPTYGNAME))

	err := ioctl(f.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&n[0])))
	if err != nil {
		return "", err
	}

	for i, c := range n {
		if c == 0 {
			return string(n[:i]), nil
		}
	}
	return "", errors.New("TIOCPTYGNAME string not NUL-terminated")
}

// Close closes the PTYs that OpenPTY created.
func (p *PTY) Close() error {
	slaveErr := errors.New("Slave FD nil")
	if p == nil {
		return errors.New("no PTY")
	}
	if p.Slave != nil {
		slaveErr = p.Slave.Close()
	}
	masterErr := errors.New("Master FD nil")
	if p.Master != nil {
		masterErr = p.Master.Close()
	}
	if slaveErr != nil || masterErr != nil {
		var errs []string
		if slaveErr != nil {
			errs = append(errs, "Slave: "+slaveErr.Error())
		}
		if masterErr != nil {
			errs = append(errs, "Master: "+masterErr.Error())
		}
		return errors.New(strings.Join(errs, " "))
	}
	return nil
}

// PTSName return the name of the pty.
func (p *PTY) PTSName() (string, error) {
	n, err := p.PTSNumber()
	if err != nil {
		return "", err
	}
	return "/dev/pts/" + strconv.Itoa(int(n)), nil
}

// PTSNumber return the pty number.
func (p *PTY) PTSNumber() (uint, error) {
	var ptyno uint
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(p.Master.Fd()), uintptr(TIOCGPTN), uintptr(unsafe.Pointer(&ptyno)))
	if errno != 0 {
		return 0, errno
	}
	return ptyno, nil
}
