package sysmount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNFSOptions(t *testing.T) {
	got := nfsOptions(51234, false)
	want := "port=51234,mountport=51234,tcp,soft,intr,timeo=600,retrans=2,nolocks,locallocks,nfc,rsize=131072,wsize=131072"
	if got != want {
		t.Errorf("got %s", got)
	}
	if ro := nfsOptions(1, true); !strings.HasSuffix(ro, ",wsize=131072,rdonly") {
		t.Errorf("read-only: %s", ro)
	}
}

func TestMountCommand(t *testing.T) {
	var gotName string
	var gotArgs []string
	d := &Darwin{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return nil, nil
	}}
	if err := d.Mount(context.Background(), 4000, "/Users/x/Volumes/a", true); err != nil {
		t.Fatal(err)
	}
	if gotName != "/sbin/mount_nfs" || gotArgs[0] != "-o" || !strings.HasSuffix(gotArgs[1], ",rdonly") || gotArgs[2] != Device || gotArgs[3] != "/Users/x/Volumes/a" {
		t.Errorf("%s %v", gotName, gotArgs)
	}
}

func TestUnmountClassification(t *testing.T) {
	cases := []struct {
		out  string
		want error
	}{
		{"Unmount failed for /x\nResource busy", ErrBusy},
		{"/x was not currently mounted", ErrNotMounted},
	}
	for _, c := range cases {
		d := &Darwin{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(c.out), errors.New("exit 1")
		}}
		err := d.Unmount(context.Background(), "/x", false)
		if !errors.Is(err, c.want) {
			t.Errorf("out %q: got %v want %v", c.out, err, c.want)
		}
	}
	var args []string
	d := &Darwin{Run: func(ctx context.Context, name string, a ...string) ([]byte, error) { args = a; return nil, nil }}
	_ = d.Unmount(context.Background(), "/x", true)
	if strings.Join(args, " ") != "unmount force /x" {
		t.Errorf("force args = %v", args)
	}
}

func TestEnsureMountPoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vol", "a")
	d := &Darwin{TableFunc: func() ([]Entry, error) {
		return []Entry{{Device: "//smb/share", MountPoint: "/Volumes/files", FSType: "smbfs"}}, nil
	}}
	if err := d.EnsureMountPoint(dir); err != nil {
		t.Fatal(err)
	}
	d.TableFunc = func() ([]Entry, error) {
		return []Entry{{Device: "//smb/share", MountPoint: dir, FSType: "smbfs"}}, nil
	}
	if err := d.EnsureMountPoint(dir); !errors.Is(err, ErrForeign) {
		t.Errorf("foreign mount: got %v", err)
	}
	d.TableFunc = func() ([]Entry, error) {
		return []Entry{{Device: Device, MountPoint: dir, FSType: "nfs"}}, nil
	}
	if err := d.EnsureMountPoint(dir); !errors.Is(err, ErrBusy) {
		t.Errorf("own leftover mount: got %v", err)
	}
}

func TestProbeLocalDir(t *testing.T) {
	d := &Darwin{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Probe(ctx, t.TempDir()); err != nil {
		t.Errorf("probe of a healthy directory must succeed (ENOENT), got %v", err)
	}
}

func TestProbeTimeout(t *testing.T) {
	d := &Darwin{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// With an already-cancelled context the select may still pick the lstat
	// result; both outcomes are acceptable, only a hang would be a bug.
	_ = d.Probe(ctx, t.TempDir())
}

func TestKernelTableReadsSomething(t *testing.T) {
	entries, err := kernelTable()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.MountPoint == "/" {
			found = true
		}
	}
	if !found {
		t.Errorf("root filesystem missing from %v", entries)
	}
}

// While an lstat on a busy volume is still under way, the next probe does
// not queue another one.
func TestProbeDoesNotPileUp(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	d := &Darwin{LstatFunc: func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-release
		return nil, os.ErrNotExist
	}}
	probe := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		return d.Probe(ctx, "/Users/x/Volumes/a")
	}
	if err := probe(); !errors.Is(err, ErrHung) {
		t.Fatalf("first probe: %v", err)
	}
	if err := probe(); !errors.Is(err, ErrHung) || calls.Load() != 1 {
		t.Fatalf("second probe: %v, lstat calls %d", err, calls.Load())
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		if err := probe(); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("probe after release: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Errorf("lstat calls = %d, want 2", calls.Load())
	}
}
