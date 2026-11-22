package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	unixPathLimit = len(syscall.RawSockaddrUnix{}.Path) - 1
	linuxOPath    = 0x200000
	procFSMagic   = 0x9fa0
)

type unixIngress struct {
	listener *net.UnixListener
	uid      uint32
}

type privateUnixListenerKey struct{}
type privateUnixPeerKey struct{}
type privateHTTPSKey struct{}

func validUnixAuthority(name string) bool {
	if len(name) > 253 || !strings.Contains(name, ".") || net.ParseIP(name) != nil {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, b := range []byte(part) {
			if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
				return false
			}
		}
	}
	return true
}

func validUnixSocketPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" &&
		len(path) <= unixPathLimit && !strings.ContainsAny(path, "\x00\r\n")
}

// Open every component relative to its already checked predecessor. A
// writable ancestor (even a sticky directory) or any link is disallowed.
func trustedUnixParent(path string, uid uint32) (int, syscall.Stat_t, error) {
	const flags = linuxOPath | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	fd, err := syscall.Open("/", flags, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, err
	}
	fail := func(err error) (int, syscall.Stat_t, error) {
		syscall.Close(fd)
		return -1, syscall.Stat_t{}, err
	}
	inspect := func() (syscall.Stat_t, error) {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return st, err
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Mode&0022 != 0 || (st.Uid != 0 && st.Uid != uid) {
			return st, errors.New("untrusted Unix socket ancestry")
		}
		return st, nil
	}
	st, err := inspect()
	if err != nil {
		return fail(err)
	}
	for part := range strings.SplitSeq(strings.TrimPrefix(filepath.Dir(path), "/"), "/") {
		if part == "" {
			continue
		}
		next, err := syscall.Openat(fd, part, flags, 0)
		if err != nil {
			return fail(err)
		}
		syscall.Close(fd)
		fd = next
		st, err = inspect()
		if err != nil {
			return fail(err)
		}
	}
	if st.Uid != uid || st.Mode&07777 != 0700 {
		return fail(errors.New("Unix socket parent must be owned by the service account with mode 0700"))
	}
	return fd, st, nil
}

func openUnixSocketLeaf(parent int, leaf string) (int, syscall.Stat_t, error) {
	fd, err := syscall.Openat(parent, leaf, linuxOPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	var st syscall.Stat_t
	if err != nil {
		return -1, st, err
	}
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return -1, st, err
	}
	return fd, st, nil
}

func sameUnixNode(a, b syscall.Stat_t) bool { return a.Dev == b.Dev && a.Ino == b.Ino }

func procFD(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

func listenProtectedUnix(path string) (*unixIngress, error) {
	if !validUnixSocketPath(path) {
		return nil, errors.New("use a canonical absolute Unix socket pathname within the Linux length limit")
	}
	uid := uint32(os.Geteuid())
	parent, original, err := trustedUnixParent(path, uid)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(parent)
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/proc/self/fd", &fs); err != nil || fs.Type != procFSMagic {
		return nil, errors.New("protected Unix ingress requires Linux procfs")
	}
	leaf := filepath.Base(path)
	addr := &net.UnixAddr{Name: procFD(parent) + "/" + leaf, Net: "unix"}
	// Linux bind refuses any existing leaf, including links or stale sockets.
	// The anchored parent cannot be substituted by a different pathname.
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, err
	}
	// Never let net close remove a stale or substituted leaf.
	listener.SetUnlinkOnClose(false)
	ready := false
	defer func() {
		if !ready {
			listener.Close()
		}
	}()
	socket, before, err := openUnixSocketLeaf(parent, leaf)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(socket)
	if before.Mode&syscall.S_IFMT != syscall.S_IFSOCK || before.Uid != uid || before.Nlink != 1 {
		return nil, errors.New("unexpected Unix socket identity")
	}
	// The enclosing directory is already 0700. Set final mode by the
	// socket inode descriptor, never by following a replaceable leaf.
	if err := os.Chmod(procFD(socket), 0600); err != nil {
		return nil, err
	}
	reopened, actual, err := trustedUnixParent(path, uid)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(reopened)
	again, after, err := openUnixSocketLeaf(reopened, leaf)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(again)
	if !sameUnixNode(original, actual) || !sameUnixNode(before, after) ||
		after.Mode != syscall.S_IFSOCK|0600 || after.Uid != uid || after.Nlink != 1 {
		return nil, errors.New("Unix socket changed during binding")
	}
	ready = true
	return &unixIngress{listener: listener, uid: uid}, nil
}

func (i *unixIngress) baseContext(listener net.Listener) context.Context {
	ctx := context.Background()
	if listener != i.listener {
		return ctx
	}
	return context.WithValue(ctx, privateUnixListenerKey{}, i)
}

func (i *unixIngress) connContext(ctx context.Context, conn net.Conn) context.Context {
	unix, ok := conn.(*net.UnixConn)
	if ctx.Value(privateUnixListenerKey{}) != i || !ok || unix.LocalAddr().String() != i.listener.Addr().String() {
		return ctx
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return ctx
	}
	var credential *syscall.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		credential, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || peerErr != nil || credential == nil || credential.Uid != i.uid {
		return ctx
	}
	return context.WithValue(ctx, privateUnixPeerKey{}, i)
}

func (i *unixIngress) wrapHTTPS(authority string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto := r.Header.Values("X-Forwarded-Proto")
		if r.Context().Value(privateUnixPeerKey{}) != i || r.Host != authority ||
			!strings.HasPrefix(r.RequestURI, "/") || r.URL.IsAbs() || r.URL.Host != "" ||
			len(proto) != 1 || proto[0] != "https" {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "Invalid protected ingress request", http.StatusBadRequest)
			return
		}
		r = r.Clone(context.WithValue(r.Context(), privateHTTPSKey{}, true))
		// Never forward proxy metadata to authentication or application code.
		for key := range r.Header {
			lower := strings.ToLower(key)
			if lower == "forwarded" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") {
				delete(r.Header, key)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func effectiveHTTPS(r *http.Request) bool {
	return r.TLS != nil || isPrivateHTTPS(r)
}

func isPrivateHTTPS(r *http.Request) bool {
	active, _ := r.Context().Value(privateHTTPSKey{}).(bool)
	return active
}
