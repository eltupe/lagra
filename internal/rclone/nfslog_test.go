package rclone

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
	"github.com/willscott/go-nfs-client/nfs/xdr"
)

// What Finder does when it copies a file onto a volume: an exclusive create
// (O_EXCL) of the file and of its AppleDouble sidecar, each retried without
// O_EXCL after the server answers NOTSUPP. The answers must stay as they
// are; the two go-nfs errors this logs are expected and go to debug.
func TestFinderCopyLogsNoErrors(t *testing.T) {
	var info, debug bytes.Buffer
	RouteLogs(fanout{
		slog.NewTextHandler(&info, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewTextHandler(&debug, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})
	defer RouteLogs(slog.NewTextHandler(io.Discard, nil))
	if err := SetCacheDir(filepath.Join(t.TempDir(), "cache")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, err := Start(ctx, Spec{Name: "finder", Type: "local", Path: t.TempDir(), HideAppleFiles: true,
		VFS: map[string]string{"vfs_cache_mode": "full"}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop(time.Second)
	c, err := rpc.DialTCP("tcp", srv.ln.Addr().(*net.TCPAddr).String(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mounter := nfsc.Mount{Client: c}
	target, err := mounter.Mount("/", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}

	answers := map[string]string{}
	for _, name := range []string{"report.pdf", "._report.pdf"} {
		answers[name+" exclusive"] = fmt.Sprint(exclusiveCreate(target, name))
		_, err := target.Create(name, 0o644)
		answers[name] = fmt.Sprint(err)
	}
	want := map[string]string{
		"report.pdf exclusive":   "NFS3ERR_NOTSUPP",
		"report.pdf":             "<nil>",
		"._report.pdf exclusive": "NFS3ERR_NOTSUPP",
		"._report.pdf":           "NFS3ERR_ACCES",
	}
	for k, w := range want {
		if !strings.Contains(answers[k], w) {
			t.Errorf("%s: got %s, want %s", k, answers[k], w)
		}
	}
	if strings.Contains(info.String(), "level=ERROR") {
		t.Errorf("expected events logged as errors:\n%s", info.String())
	}
	if !strings.Contains(debug.String(), "refuse Finder metadata ._report.pdf: permission denied") ||
		strings.Count(debug.String(), "exclusive") != 2 {
		t.Errorf("debug log lacks the events:\n%s", debug.String())
	}
}

// A permission problem that is not the filter's stays an error.
func TestOtherCreateErrorsStayErrors(t *testing.T) {
	if isExpectedNFS("Error Creating: permission denied") || isExpectedNFS("Error writing: no space left on device") {
		t.Error("only lagra's own refusals and the exclusive-create fallback are expected")
	}
}

// fanout sends every record to several handlers.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r)
		}
	}
	return nil
}

func (f fanout) WithAttrs([]slog.Attr) slog.Handler { return f }
func (f fanout) WithGroup(string) slog.Handler      { return f }

// exclusiveCreate sends CREATE with createmode EXCLUSIVE, as macOS does
// for open(O_CREAT|O_EXCL).
func exclusiveCreate(v *nfsc.Target, name string) error {
	_, fh, err := v.Lookup("")
	if err != nil {
		return err
	}
	type args struct {
		rpc.Header
		Where nfsc.Diropargs3
		Mode  uint32
		Verf  [8]byte
	}
	res, err := v.Call(&args{
		Header: rpc.Header{Rpcvers: 2, Prog: nfsc.Nfs3Prog, Vers: nfsc.Nfs3Vers, Proc: nfsc.NFSProc3Create, Cred: rpc.AuthNull, Verf: rpc.AuthNull},
		Where:  nfsc.Diropargs3{FH: fh, Filename: name},
		Mode:   2,
		Verf:   [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
	})
	if err != nil {
		return err
	}
	status, err := xdr.ReadUint32(res)
	if err != nil {
		return err
	}
	return nfsc.NFS3Error(status)
}
