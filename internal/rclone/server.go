package rclone

import (
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudsoda/go-smb2"
	gonfs "github.com/willscott/go-nfs"

	webdavapi "github.com/rclone/rclone/backend/webdav/api"
	"github.com/rclone/rclone/cmd/serve/nfs"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/vfs"
	"github.com/rclone/rclone/vfs/vfscommon"

	"github.com/eltupe/lagra/internal/sysmount"
)

// Spec is everything needed to serve one mount. Options already contain the
// resolved secrets; Spec must therefore never be logged as a whole.
type Spec struct {
	Name           string
	Type           string
	Path           string
	Options        map[string]string
	VFS            map[string]string
	ReadOnly       bool
	HandleCacheDir string // per-mount directory for the NFS handle cache
	HideAppleFiles bool   // refuse and hide ._*, .DS_Store and friends
}

// Server is one running NFS server for one mount. It uses rclone's NFS
// handler (VFS adapter plus handle cache) on a go-nfs listener that lagra
// owns, so the Apple-litter filter can sit in between.
type Server struct {
	f    fs.Fs
	vfs  *vfs.VFS
	ln   net.Listener
	port int

	mu       sync.Mutex
	serveErr error
	done     chan struct{}
}

// Starter starts servers; the engine depends on this rather than on *Server
// so it can be tested with fakes and so an rc-based implementation could
// replace the in-process one (see ADR 0002).
type Starter interface {
	Start(ctx context.Context, spec Spec) (Instance, error)
}

// Instance is a running server as the engine sees it.
type Instance interface {
	Port() int
	// Probe asks the backend a cheap question within the context deadline.
	Probe(ctx context.Context) error
	// Sync expedites every queued upload and drops the directory cache so
	// changes made elsewhere become visible. Returns the number of uploads
	// that were expedited.
	Sync(ctx context.Context) (int, error)
	// ProbeServer asks the NFS server itself, not the volume, whether it answers.
	ProbeServer(ctx context.Context) error
	// Uploads returns the number of pending uploads (queued + in progress).
	Uploads() int
	// Stop flushes writers for at most wait, then shuts down.
	Stop(wait time.Duration) error
	// Err reports a serve loop failure, if any.
	Err() error
}

// InProcess starts servers inside this process.
type InProcess struct{}

// Start implements Starter. The context deadline bounds backend creation,
// which for S3 performs a network request with SDK retries.
func (InProcess) Start(ctx context.Context, spec Spec) (Instance, error) {
	return Start(ctx, spec)
}

// Start creates the backend, the VFS and the NFS server on a random
// localhost port and begins serving.
func Start(ctx context.Context, spec Spec) (*Server, error) {
	ri, err := fs.Find(spec.Type)
	if err != nil {
		return nil, fmt.Errorf("backend %q: %w", spec.Type, err)
	}
	// Validation sees only hosts kept in the file, not those from secrets.
	if err := checkHost(ri.Name, spec.Options["host"]); err != nil {
		return nil, err
	}
	values := configmap.Simple{}
	maps.Copy(values, recommended[spec.Type])
	maps.Copy(values, spec.Options) // the account's own values win
	if err := obscurePasswords(ri.Options, values); err != nil {
		return nil, err
	}
	// fs.ConfigMap layers rclone's defaults beneath our values; the empty
	// config name keeps rclone.conf out of the lookup.
	cm := fs.ConfigMap(ri.Prefix, ri.Options, "", values)
	// The backend takes its retry count from this context when it opens.
	if n, ok := connectRetries[ri.Name]; ok {
		var ci *fs.ConfigInfo
		ctx, ci = fs.AddConfig(ctx)
		ci.LowLevelRetries = n
	}

	type result struct {
		f   fs.Fs
		err error
	}
	ch := make(chan result, 1)
	// rclone's SMB backend connects in NewFs to find out whether a path
	// with a folder is a file, unless the path ends in a slash. lagra only
	// mounts folders, and a server that cannot be reached would hold up
	// the start; the root check below asks within a bounded time.
	root := spec.Path
	if ri.Name == "smb" && root != "" && !strings.HasSuffix(root, "/") {
		root += "/"
	}
	go func() {
		f, err := ri.NewFs(ctx, "lagra-"+spec.Name, root, cm)
		ch <- result{f, err}
	}()
	var f fs.Fs
	select {
	case r := <-ch:
		if r.err != nil {
			if errors.Is(r.err, fs.ErrorIsFile) {
				return nil, fmt.Errorf("path %q is a file, not a directory", spec.Path)
			}
			return nil, fmt.Errorf("open %s:%s: %w", spec.Type, spec.Path, loginRejected(r.err))
		}
		f = r.f
	case <-ctx.Done():
		return nil, fmt.Errorf("open %s:%s: %w", spec.Type, spec.Path, ctx.Err())
	}
	// A bucket or folder that does not exist would show up as an empty
	// volume, and a rejected login would be repeated by every request.
	// Any other error (offline, no list permission) is left to the probe,
	// so that the volume can still come up offline.
	if err := loginRejected(checkRoot(ctx, f)); errors.Is(err, fs.ErrorDirNotFound) || smbShareMissing(err) {
		return nil, &PathNotFoundError{Path: spec.Path}
	} else if _, ok := errors.AsType[*LoginRejectedError](err); ok {
		return nil, err
	} else if err != nil {
		fs.Debugf(nil, "lagra: checking %s:%s: %v", spec.Type, spec.Path, err)
	}

	vopt := vfscommon.Opt
	if err := configstruct.Set(configmap.Simple(spec.VFS), &vopt); err != nil {
		return nil, fmt.Errorf("vfs options: %w", err)
	}
	vopt.ReadOnly = spec.ReadOnly
	vopt.Init(context.Background())
	v := vfs.New(context.Background(), f, &vopt)

	nopt := nfs.Opt
	if spec.HandleCacheDir != "" {
		nopt.HandleCacheDir = spec.HandleCacheDir
		if err := configstruct.Set(configmap.Simple{"nfs_cache_type": "disk"}, &nopt); err != nil {
			v.Shutdown()
			return nil, fmt.Errorf("nfs options: %w", err)
		}
	}
	handler, err := nfs.NewHandler(context.Background(), v, &nopt)
	if err != nil {
		v.Shutdown()
		return nil, fmt.Errorf("nfs handler: %w", err)
	}
	handler = newVolumeHandler(handler, spec.HideAppleFiles)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		v.Shutdown()
		return nil, fmt.Errorf("nfs listener: %w", err)
	}
	s := &Server{f: f, vfs: v, ln: ln, port: ln.Addr().(*net.TCPAddr).Port, done: make(chan struct{})}
	go func() {
		srv := &gonfs.Server{Handler: handler, ConcurrentHandlers: concurrentRequests, EnabledNFSVersions: []uint32{3}}
		err := srv.Serve(ln)
		s.mu.Lock()
		if err != nil && !errors.Is(err, net.ErrClosed) {
			s.serveErr = err
		}
		s.mu.Unlock()
		close(s.done)
	}()
	return s, nil
}

// concurrentRequests is how many requests of one volume the server works
// on at once. macOS sends everything for a volume over one connection; one
// at a time, a single slow request (listing a large folder, waiting for a
// download) would hold up every other one, the mount probe included, until
// macOS reports the server as not responding. rclone's handler and VFS are
// safe for concurrent use.
const concurrentRequests = 16

// PathNotFoundError reports a mount path that does not exist on the
// storage. Retrying cannot fix it, so the engine shows the mount as failed.
type PathNotFoundError struct {
	Path string
}

func (e *PathNotFoundError) Error() string {
	return fmt.Sprintf("bucket, share or folder %q does not exist on the storage", e.Path)
}

// Unwrap makes errors.Is(err, fs.ErrorDirNotFound) hold.
func (e *PathNotFoundError) Unwrap() error { return fs.ErrorDirNotFound }

// LoginRejectedError reports that the storage refused the login. Trying
// again with the same credentials only adds failed logins, which a
// directory service such as Active Directory counts towards locking the
// account, so the engine stops the mount instead of retrying.
type LoginRejectedError struct {
	Err error
}

func (e *LoginRejectedError) Error() string {
	return "the server rejected the login: " + e.Err.Error()
}

func (e *LoginRejectedError) Unwrap() error { return e.Err }

// smbLoginRefused are the NTSTATUS codes ([MS-ERREF] 2.3.1) with which an
// SMB server refuses a session: wrong user or password, and accounts that
// are locked, disabled, expired or restricted. rclone's SMB backend uses
// go-smb2, which reports them as *smb2.ResponseError.
var smbLoginRefused = map[uint32]bool{
	0xC0000064: true, // STATUS_NO_SUCH_USER
	0xC000006A: true, // STATUS_WRONG_PASSWORD
	0xC000006D: true, // STATUS_LOGON_FAILURE
	0xC000006E: true, // STATUS_ACCOUNT_RESTRICTION
	0xC000006F: true, // STATUS_INVALID_LOGON_HOURS
	0xC0000070: true, // STATUS_INVALID_WORKSTATION
	0xC0000071: true, // STATUS_PASSWORD_EXPIRED
	0xC0000072: true, // STATUS_ACCOUNT_DISABLED
	0xC0000193: true, // STATUS_ACCOUNT_EXPIRED
	0xC0000224: true, // STATUS_PASSWORD_MUST_CHANGE
	0xC0000234: true, // STATUS_ACCOUNT_LOCKED_OUT
}

// loginRejected wraps err in a LoginRejectedError when it says the storage
// refused the login, and returns it unchanged otherwise: an SMB session
// refused with one of smbLoginRefused, or a WebDAV request answered with
// 401, which servers behind a directory (Basic auth against Active
// Directory or LDAP) count as a failed login too.
func loginRejected(err error) error {
	var re *smb2.ResponseError
	var de *webdavapi.Error
	switch {
	case errors.As(err, &re) && smbLoginRefused[re.Code],
		errors.As(err, &de) && de.StatusCode == http.StatusUnauthorized:
		return &LoginRejectedError{Err: err}
	}
	return err
}

// smbShareMissing reports a share the SMB server does not have
// (STATUS_BAD_NETWORK_NAME). A domain-based DFS name (\\domain\namespace)
// ends here too: go-smb2 follows no DFS referrals.
func smbShareMissing(err error) bool {
	var re *smb2.ResponseError
	return errors.As(err, &re) && re.Code == 0xC00000CC
}

// connectRetries caps how often a backend opens a connection before it
// gives up, where rclone's low_level_retries (10) would repeat failed
// logins. rclone's SMB backend retries every failed connection, a rejected
// login included, and opens one for every request when none is pooled,
// which with a wrong password is always: each Finder request cost ten
// failed logins, and Active Directory typically locks an account after
// five to ten. Its pacer is used for nothing but opening connections, so this
// does not touch file operations. See ADR 0001.
var connectRetries = map[string]int{"smb": 1}

// rootCheckTimeout bounds checkRoot, so that an unreachable storage delays
// an offline mount only briefly.
const rootCheckTimeout = 3 * time.Second

var errEnough = errors.New("enough")

// checkRoot lists the start of the mount's root: fs.ErrorDirNotFound when
// it does not exist. For S3 this also covers a missing folder inside a
// bucket, because lagra keeps directory markers on. It stops after the
// first batch of entries, so a large root costs one request.
func checkRoot(ctx context.Context, f fs.Fs) error {
	ctx, cancel := context.WithTimeout(ctx, rootCheckTimeout)
	defer cancel()
	var err error
	if listP := f.Features().ListP; listP != nil {
		err = listP(ctx, "", func(fs.DirEntries) error { return errEnough })
	} else {
		_, err = f.List(ctx, "")
	}
	if errors.Is(err, errEnough) {
		return nil
	}
	return err
}

// Port implements Instance.
func (s *Server) Port() int { return s.port }

// ProbeServer implements Instance: the NFS NULL procedure on a connection
// of its own, so it shows whether the server works even while the volume's
// own connection is busy.
func (s *Server) ProbeServer(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.ln.Addr().String())
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	const xid = 0x6c616772 // "lagr"
	// One record-marked ONC RPC call: xid, CALL, RPC version 2, program
	// NFS (100003) version 3, procedure NULL, AUTH_NONE credential and
	// verifier.
	call := []uint32{1<<31 | 40, xid, 0, 2, 100003, 3, 0, 0, 0, 0, 0}
	if err := binary.Write(conn, binary.BigEndian, call); err != nil {
		return err
	}
	// Reply: record mark, xid, REPLY, MSG_ACCEPTED, verifier (AUTH_NONE,
	// empty), accept_stat.
	var reply [7]uint32
	if err := binary.Read(conn, binary.BigEndian, &reply); err != nil {
		return err
	}
	if reply[1] != xid || reply[2] != 1 || reply[3] != 0 || reply[6] != 0 {
		return fmt.Errorf("nfs server answered NULL with %v", reply[1:])
	}
	return nil
}

// Probe implements Instance with a HEAD-style lookup of a name that does not
// exist: cheap, and "not found" proves the backend answered.
func (s *Server) Probe(ctx context.Context) error {
	_, err := s.f.NewObject(ctx, sysmount.ProbePrefix+strconv.FormatInt(time.Now().UnixNano(), 36))
	switch {
	case err == nil, errors.Is(err, fs.ErrorObjectNotFound), errors.Is(err, fs.ErrorIsDir), errors.Is(err, fs.ErrorNotAFile):
		return nil
	}
	return loginRejected(err)
}

// recommended are backend options lagra applies unless the mount sets them,
// because a Finder volume behaves differently from a sync tool. Together with
// bucketBased in registry.go, the only backend-specific knowledge outside
// rclone's own metadata (see ADR 0001).
var recommended = map[string]map[string]string{
	// S3 has no directories; markers keep empty folders alive as "name/" objects.
	"s3": {"directory_markers": "true"},
}

// obscurePasswords obscures the options rclone marks as passwords. rclone
// keeps them obscured in its config file and reveals them when it opens the
// backend; lagra's come from the keychain in the clear.
func obscurePasswords(opts fs.Options, values configmap.Simple) error {
	for _, o := range opts {
		if v := values[o.Name]; o.IsPassword && v != "" {
			ov, err := obscure.Obscure(v)
			if err != nil {
				return fmt.Errorf("option %q: %w", o.Name, err)
			}
			values[o.Name] = ov
		}
	}
	return nil
}

// Sync implements Instance through rclone's rc calls, which are the only
// exported way to reach the VFS write-back queue.
func (s *Server) Sync(ctx context.Context) (int, error) {
	fsName := fs.ConfigString(s.f)
	n := 0
	if call := rc.Calls.Get("vfs/queue"); call != nil {
		out, err := call.Fn(ctx, rc.Params{"fs": fsName})
		if err != nil {
			return 0, fmt.Errorf("vfs/queue: %w", err)
		}
		if setExpiry := rc.Calls.Get("vfs/queue-set-expiry"); setExpiry != nil {
			for _, id := range queueIDs(out) {
				if _, err := setExpiry.Fn(ctx, rc.Params{"fs": fsName, "id": id, "expiry": 0}); err == nil {
					n++
				}
			}
		}
	}
	s.vfs.FlushDirCache()
	return n, nil
}

// queueIDs extracts item ids from a vfs/queue result without depending on
// the concrete slice type rclone uses.
func queueIDs(out rc.Params) []int64 {
	var ids []int64
	raw, ok := out["queue"]
	if !ok {
		return nil
	}
	// The value is []writeback.QueueInfo; go through JSON to stay decoupled.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var items []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(b, &items); err != nil {
		return nil
	}
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

// Uploads implements Instance: the files in rclone's write-back queue,
// waiting or uploading. It asks the queue (rc vfs/queue) rather than
// vfs.Stats, which walks every directory under that directory's lock and so
// waits as long as a listing against a slow or frozen storage does: minutes,
// with the coordinator stuck behind it. Without a cache (vfs_cache_mode off)
// nothing is pending.
func (s *Server) Uploads() int {
	call := rc.Calls.Get("vfs/queue")
	if call == nil {
		return 0
	}
	out, err := call.Fn(context.Background(), rc.Params{"fs": fs.ConfigString(s.f)})
	if err != nil {
		return 0
	}
	return len(queueIDs(out))
}

// Stop implements Instance.
func (s *Server) Stop(wait time.Duration) error {
	if wait > 0 {
		// WaitForWriters walks the directories under their locks too, so a
		// listing stuck on the storage must not hold up the stop.
		done := make(chan struct{})
		go func() { s.vfs.WaitForWriters(wait); close(done) }()
		select {
		case <-done:
		case <-time.After(wait + time.Second):
		}
	}
	err := s.ln.Close()
	s.vfs.Shutdown()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// Err implements Instance.
func (s *Server) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveErr
}
