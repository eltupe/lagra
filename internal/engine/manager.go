package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/sysmount"
)

// Manager owns all coordinators and applies configuration diffs to them.
type Manager struct {
	deps Deps

	mu      sync.Mutex
	current *config.Effective
	coords  map[string]*Coordinator
	// removing holds coordinators gone from the file whose removal waits
	// for uploads or a busy volume; they still count for status and quit.
	removing map[*Coordinator]bool
}

// NewManager creates a manager with no mounts.
func NewManager(deps Deps) *Manager {
	deps.defaults()
	return &Manager{deps: deps, coords: map[string]*Coordinator{}, removing: map[*Coordinator]bool{}}
}

// Configure adjusts dependencies (cache directories, thresholds). The
// upload-stuck threshold also reaches running coordinators.
func (m *Manager) Configure(fn func(*Deps)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.deps)
	m.deps.defaults()
	for _, c := range m.coords {
		c.configure(m.deps.UploadStuck)
	}
}

// CleanOrphans handles mounts recorded in the state file whose server died
// with a previous process: their NFS mounts are stale and are force-unmounted.
// Anything not recorded there is foreign and untouched. Call once at startup,
// before Apply.
func (m *Manager) CleanOrphans(ctx context.Context) error {
	if m.deps.State == nil {
		return nil
	}
	st, err := m.deps.State.Load()
	if err != nil {
		return err
	}
	if len(st.Mounts) == 0 {
		return nil
	}
	table, err := m.deps.Sys.Table()
	if err != nil {
		return err
	}
	mounted := map[string]sysmount.Entry{}
	for _, e := range table {
		mounted[e.MountPoint] = e
	}
	for name, rec := range st.Mounts {
		if e, ok := mounted[rec.MountPoint]; ok && e.Ours() {
			m.deps.Log.Info("cleaning orphaned mount", "mount", name, "mount_point", rec.MountPoint)
			uctx, cancel := context.WithTimeout(ctx, m.deps.UnmountTimeout)
			err := m.deps.Sys.Unmount(uctx, rec.MountPoint, true)
			cancel()
			if err != nil && !errors.Is(err, sysmount.ErrNotMounted) {
				m.deps.Log.Warn("orphan cleanup failed", "mount", name, "err", err.Error())
				continue
			}
		}
		delete(st.Mounts, name)
	}
	return m.deps.State.Save(st)
}

// Apply moves the set of coordinators to the new configuration: added mounts
// connect, removed ones disconnect and go away, restart changes reconnect,
// unchanged mounts are not touched. Returns the diff for logging.
func (m *Manager) Apply(eff *config.Effective) config.Diff {
	m.mu.Lock()
	defer m.mu.Unlock()
	diff := config.Compute(m.current, eff)
	newDefs := map[string]config.EffectiveMount{}
	for _, d := range eff.Mounts {
		newDefs[d.Name] = d
	}
	// Mounts gone from the file
	for name, c := range m.coords {
		if _, still := newDefs[name]; !still {
			m.removing[c] = true
			c.Remove(false)
			delete(m.coords, name)
		}
	}
	for name, def := range newDefs {
		if c, exists := m.coords[name]; exists {
			c.Update(def)
			continue
		}
		var c *Coordinator
		c = NewCoordinator(m.deps, def, func() { go m.forget(c) })
		m.coords[name] = c
		// A mount added back under the same name waits until the old
		// coordinator has let go of the volume.
		if def.Enabled && !m.removingName(name) {
			c.Connect(false)
		}
	}
	m.current = eff
	return diff
}

// forget drops a coordinator whose removal finished and starts a successor
// with the same name that waited for it.
func (m *Manager) forget(c *Coordinator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.removing[c] {
		return
	}
	delete(m.removing, c)
	name := c.Snapshot().Name
	if n, ok := m.coords[name]; ok && !m.removingName(name) && m.enabled(name) {
		n.Connect(false)
	}
}

func (m *Manager) removingName(name string) bool {
	for c := range m.removing {
		if c.Snapshot().Name == name {
			return true
		}
	}
	return false
}

func (m *Manager) enabled(name string) bool {
	if m.current == nil {
		return false
	}
	for _, d := range m.current.Mounts {
		if d.Name == name {
			return d.Enabled
		}
	}
	return false
}

// Snapshots returns all mounts sorted by name, including removals that are
// still waiting, unless a mount of the same name took their place.
func (m *Manager) Snapshots() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.coords)+len(m.removing))
	for _, c := range m.coords {
		out = append(out, c.Snapshot())
	}
	for c := range m.removing {
		if s := c.Snapshot(); m.coords[s.Name] == nil {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Snapshot) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Get returns one coordinator.
func (m *Manager) Get(name string) (*Coordinator, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.coords[name]
	return c, ok
}

// ProbeAll triggers immediate probes on every mount (wake, network change).
func (m *Manager) ProbeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.coords {
		c.ProbeNow()
	}
}

// PendingUploads sums pending uploads across mounts, waiting removals
// included.
func (m *Manager) PendingUploads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.coords {
		n += c.Snapshot().Uploads
	}
	for c := range m.removing {
		n += c.Snapshot().Uploads
	}
	return n
}

// Shutdown disconnects every mount (forcing after a clean attempt) and waits.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	coords := make([]*Coordinator, 0, len(m.coords)+len(m.removing))
	for _, c := range m.coords {
		coords = append(coords, c)
	}
	for c := range m.removing {
		coords = append(coords, c)
	}
	m.coords = map[string]*Coordinator{}
	m.removing = map[*Coordinator]bool{}
	m.current = nil
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range coords {
		wg.Go(func() { c.Shutdown(ctx) }) // each gives up when ctx is done
	}
	wg.Wait()
}

// Overall summarises all mounts for the menu bar icon.
type Overall string

const (
	OverallIdle    Overall = "idle"    // nothing connected or on the way
	OverallOK      Overall = "ok"      // something connected, nothing amiss
	OverallPartial Overall = "partial" // some connecting/offline/pending
	OverallError   Overall = "error"   // at least one broken or failed
)

// Summarize computes the overall state from snapshots.
func Summarize(snaps []Snapshot) Overall {
	out := OverallIdle
	for _, s := range snaps {
		switch s.State {
		case Broken, Failed:
			return OverallError
		case Connecting, Offline:
			out = OverallPartial
		case Connected:
			if out == OverallIdle {
				out = OverallOK
			}
			if s.Pending != nil && out == OverallOK {
				out = OverallPartial
			}
		}
	}
	return out
}
