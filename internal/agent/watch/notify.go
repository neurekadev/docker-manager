package watch

import (
	"errors"

	"github.com/fsnotify/fsnotify"
)

// Op is a set of filesystem operations reported for one path.
type Op uint8

// Operations.
const (
	OpCreate Op = 1 << iota
	OpWrite
	OpRemove
	OpRename
	OpChmod
)

// Event is one kernel notification: Path is the absolute (OS) path of the
// changed entry.
type Event struct {
	Path string
	Op   Op
}

// ErrOverflow is reported on Errors when the kernel dropped notifications
// (inotify queue overflow): every scope in inotify mode is reconciled.
var ErrOverflow = errors.New("watch: kernel notification queue overflowed")

// Notifier is the kernel notification source: one non-recursive watch per
// directory. The production implementation is fsnotify (inotify on Linux);
// tests use a fake. Add and Remove may be called from any goroutine.
type Notifier interface {
	Add(dir string) error
	Remove(dir string) error
	Events() <-chan Event
	Errors() <-chan error
	Close() error
}

// notifyBuffer is the fsnotify event buffer (events are also queued by the
// kernel: 16 384 per inotify instance by default).
const notifyBuffer = 4096

// NewFSNotifier returns an fsnotify-backed Notifier. It fails when the
// kernel refuses a notification instance (for example
// fs.inotify.max_user_instances reached): scopes are then polled.
func NewFSNotifier() (Notifier, error) {
	w, err := fsnotify.NewBufferedWatcher(notifyBuffer)
	if err != nil {
		return nil, err
	}
	n := &fsNotifier{w: w, events: make(chan Event, notifyBuffer), errs: make(chan error, 16), done: make(chan struct{})}
	go n.pump()
	return n, nil
}

type fsNotifier struct {
	w      *fsnotify.Watcher
	events chan Event
	errs   chan error
	done   chan struct{}
}

func (n *fsNotifier) pump() {
	defer close(n.events)
	for {
		select {
		case <-n.done:
			return
		case e, ok := <-n.w.Events:
			if !ok {
				return
			}
			var op Op
			if e.Has(fsnotify.Create) {
				op |= OpCreate
			}
			if e.Has(fsnotify.Write) {
				op |= OpWrite
			}
			if e.Has(fsnotify.Remove) {
				op |= OpRemove
			}
			if e.Has(fsnotify.Rename) {
				op |= OpRename
			}
			if e.Has(fsnotify.Chmod) {
				op |= OpChmod
			}
			select {
			case n.events <- Event{Path: e.Name, Op: op}:
			case <-n.done:
				return
			}
		case err, ok := <-n.w.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				err = ErrOverflow
			}
			select {
			case n.errs <- err:
			default: // a burst of errors: one pending is enough
			}
		}
	}
}

func (n *fsNotifier) Add(dir string) error    { return n.w.Add(dir) }
func (n *fsNotifier) Remove(dir string) error { return n.w.Remove(dir) }
func (n *fsNotifier) Events() <-chan Event    { return n.events }
func (n *fsNotifier) Errors() <-chan error    { return n.errs }

func (n *fsNotifier) Close() error {
	select {
	case <-n.done:
		return nil
	default:
	}
	close(n.done)
	return n.w.Close()
}
