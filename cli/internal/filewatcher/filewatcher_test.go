package filewatcher

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/vercel/turbo/cli/internal/fs"
	"github.com/vercel/turbo/cli/internal/turbopath"
	"gotest.tools/v3/assert"
)

type testClient struct {
	mu           sync.Mutex
	createEvents []Event
	notify       chan Event
}

func (c *testClient) OnFileWatchEvent(ev Event) {
	if ev.EventType == FileAdded {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.createEvents = append(c.createEvents, ev)
		c.notify <- ev
	}
}

func (c *testClient) OnFileWatchError(err error) {}

func (c *testClient) OnFileWatchClosed() {}

func expectFilesystemEvent(t *testing.T, ch <-chan Event, expected Event) {
	// mark this method as a helper
	t.Helper()
	timeout := time.After(1 * time.Second)
	for {
		select {
		case ev := <-ch:
			t.Logf("got event %v", ev)
			if ev.Path == expected.Path && ev.EventType == expected.EventType {
				return
			}
		case <-timeout:
			t.Errorf("Timed out waiting for filesystem event at %v %v", expected.EventType, expected.Path)
			return
		}
	}
}

func expectNoFilesystemEvent(t *testing.T, ch <-chan Event) {
	// mark this method as a helper
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Errorf("got unexpected filesystem event %v", ev)
		} else {
			t.Error("filewatching closed unexpectedly")
		}
	case <-time.After(500 * time.Millisecond):
		return
	}
}

func expectWatching(t *testing.T, c *testClient, dirs []turbopath.AbsoluteSystemPath) {
	t.Helper()
	now := time.Now()
	filename := fmt.Sprintf("test-%v", now.UnixMilli())
	for _, dir := range dirs {
		file := dir.UntypedJoin(filename)
		err := file.WriteFile([]byte("hello"), 0755)
		assert.NilError(t, err, "WriteFile")
		expectFilesystemEvent(t, c.notify, Event{
			Path:      file,
			EventType: FileAdded,
		})
	}
}

func TestFileWatching(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	repoRoot := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("node_modules", "some-dep").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("parent", "child").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("parent", "sibling").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	// Directory layout:
	// <repoRoot>/
	//	 .git/
	//   node_modules/
	//     some-dep/
	//   parent/
	//     child/
	//     sibling/

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
		repoRoot.UntypedJoin("parent"),
		repoRoot.UntypedJoin("parent", "child"),
		repoRoot.UntypedJoin("parent", "sibling"),
	}
	expectWatching(t, c, expectedWatching)

	fooPath := repoRoot.UntypedJoin("parent", "child", "foo")
	err = fooPath.WriteFile([]byte("hello"), 0644)
	assert.NilError(t, err, "WriteFile")
	expectFilesystemEvent(t, ch, Event{
		EventType: FileAdded,
		Path:      fooPath,
	})

	deepPath := repoRoot.UntypedJoin("parent", "sibling", "deep", "path")
	err = deepPath.MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	// We'll catch an event for "deep", but not "deep/path" since
	// we don't have a recursive watch
	expectFilesystemEvent(t, ch, Event{
		Path:      repoRoot.UntypedJoin("parent", "sibling", "deep"),
		EventType: FileAdded,
	})
	expectFilesystemEvent(t, ch, Event{
		Path:      repoRoot.UntypedJoin("parent", "sibling", "deep", "path"),
		EventType: FileAdded,
	})
	expectedWatching = append(expectedWatching, deepPath, repoRoot.UntypedJoin("parent", "sibling", "deep"))
	expectWatching(t, c, expectedWatching)

	gitFilePath := repoRoot.UntypedJoin(".git", "git-file")
	err = gitFilePath.WriteFile([]byte("nope"), 0644)
	assert.NilError(t, err, "WriteFile")
	expectNoFilesystemEvent(t, ch)
}

// TestFileWatchingParentDeletion tests that when a repo subfolder is deleted,
// recursive watching will still work for new folders
//
// ✅ macOS
// ✅ Linux
// ✅ Windows
func TestFileWatchingSubfolderDeletion(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	repoRoot := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("node_modules", "some-dep").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("parent", "child").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	// Directory layout:
	// <repoRoot>/
	//	 .git/
	//   node_modules/
	//     some-dep/
	//   parent/
	//     child/

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
		repoRoot.UntypedJoin("parent"),
		repoRoot.UntypedJoin("parent", "child"),
	}
	expectWatching(t, c, expectedWatching)

	// Delete parent folder during file watching
	err = repoRoot.UntypedJoin("parent").RemoveAll()
	assert.NilError(t, err, "RemoveAll")

	// Ensure we don't get any event when creating file in deleted directory
	folder := repoRoot.UntypedJoin("parent", "child")
	err = os.MkdirAll(folder.ToString(), 0775)
	assert.NilError(t, err, "MkdirAll")

	expectFilesystemEvent(t, ch, Event{
		EventType: FileAdded,
		Path:      repoRoot.UntypedJoin("parent"),
	})

	expectFilesystemEvent(t, ch, Event{
		EventType: FileAdded,
		Path:      folder,
	})

	fooPath := folder.UntypedJoin("foo")
	err = fooPath.WriteFile([]byte("hello"), 0644)
	assert.NilError(t, err, "WriteFile")

	expectFilesystemEvent(t, ch, Event{
		EventType: FileAdded,
		Path:      folder.UntypedJoin("foo"),
	})

	expectNoFilesystemEvent(t, ch)
}

// TestFileWatchingRootDeletion tests that when the root is deleted,
// file watching will continue, and no deletion event will be sent.
//
// It additonally tests that when the root is recreated, file watching
// will continue, and an add event will be sent for the re-created root.
//
// ✅ macOS
// ❌ Linux - we do not get an event when the root is recreated L287
// ❌ Windows - we do not get an event when the root is recreated L287
func TestFileWatchingRootDeletion(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	repoRoot := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("node_modules", "some-dep").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("parent", "child").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	// Directory layout:
	// <repoRoot>/
	//	 .git/
	//   node_modules/
	//     some-dep/
	//   parent/
	//     child/

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
		repoRoot.UntypedJoin("parent"),
		repoRoot.UntypedJoin("parent", "child"),
	}
	expectWatching(t, c, expectedWatching)

	// Delete parent folder during file watching
	err = repoRoot.RemoveAll()
	assert.NilError(t, err, "RemoveAll")

	expectNoFilesystemEvent(t, ch)

	// Ensure we don't get any event when creating file in deleted directory
	err = repoRoot.MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	expectFilesystemEvent(t, ch, Event{
		EventType: FileAdded,
		Path:      repoRoot,
	})
}

// TestFileWatchingSubfolderRename tests that when a repo subfolder is renamed,
// file watching will continue, and a rename event will be sent.
//
// ❌ macOS - rename events are not being sent
// ❌ Linux - renaming generates file creation events of the new folder (and all the subcontents), not a rename
// ❌ Windows - you cannot rename a watched folder (see https://github.com/fsnotify/fsnotify/issues/356)
func TestFileWatchingSubfolderRename(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	repoRoot := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("node_modules", "some-dep").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("parent", "child").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	// Directory layout:
	// <repoRoot>/
	//	 .git/
	//   node_modules/
	//     some-dep/
	//   parent/
	//     child/

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
		repoRoot.UntypedJoin("parent"),
		repoRoot.UntypedJoin("parent", "child"),
	}
	expectWatching(t, c, expectedWatching)

	// Rename parent folder during file watching
	err = os.Rename(string(repoRoot.UntypedJoin("parent")), string(repoRoot.UntypedJoin("new_parent")))
	assert.NilError(t, err, "Rename")
	expectFilesystemEvent(t, ch, Event{
		EventType: FileRenamed,
		Path:      repoRoot.UntypedJoin("new_parent"),
	})

	// Ensure we get an event when creating a file in renamed directory
	fooPath := repoRoot.UntypedJoin("new_parent", "child", "foo")
	err = fooPath.WriteFile([]byte("hello"), 0644)
	assert.NilError(t, err, "WriteFile")
	expectFilesystemEvent(t, ch, Event{
		EventType: FileAdded,
		Path:      fooPath,
	})
}

// TestFileWatchingRootRename tests that when the root is renamed,
// file watching will stop watching that directory, and no new events
// will be sent.
//
// It additonally tests that when the root folder is renamed back to its
// original name, file watching will continue, and a rename event will be sent.
//
// ✅ macOS
// ❌ Linux - L415 fails because renames are respected and creating a file emits an event
// ❌ Windows - you cannot rename a watched folder (see https://github.com/fsnotify/fsnotify/issues/356)
func TestFileWatchingRootRename(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	oldRepoRoot := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	err := oldRepoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = oldRepoRoot.UntypedJoin("node_modules", "some-dep").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = oldRepoRoot.UntypedJoin("parent", "child").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	// Directory layout:
	// <oldRepoRoot>/
	//	 .git/
	//   node_modules/
	//     some-dep/
	//   parent/
	//     child/

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, oldRepoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		oldRepoRoot,
		oldRepoRoot.UntypedJoin("parent"),
		oldRepoRoot.UntypedJoin("parent", "child"),
	}
	expectWatching(t, c, expectedWatching)

	// Rename root folder during file watching
	newRepoRoot := oldRepoRoot.Dir().UntypedJoin("new_repo_root")
	err = os.Rename(string(oldRepoRoot), string(newRepoRoot))
	assert.NilError(t, err, "Rename")

	expectNoFilesystemEvent(t, ch)

	// Ensure we get no event when creating a file in the renamed directory
	fooPath := newRepoRoot.UntypedJoin("parent", "child", "foo")
	err = fooPath.WriteFile([]byte("hello"), 0644)
	assert.NilError(t, err, "WriteFile")

	expectNoFilesystemEvent(t, ch)

	// Rename root folder back to original name
	err = os.Rename(string(newRepoRoot), string(oldRepoRoot))
	assert.NilError(t, err, "Rename")

	expectNoFilesystemEvent(t, ch)

	// Ensure we get an event when creating a file in the renamed directory
	fooPath = oldRepoRoot.UntypedJoin("parent", "child", "foo")
	err = fooPath.WriteFile([]byte("hello"), 0644)
	assert.NilError(t, err, "WriteFile")

	// file watching has stopped
	expectNoFilesystemEvent(t, ch)
}

// TestFileWatchSymlinkCreate tests that when a symlink is created,
// file watching will continue, and a file create event is sent.
// it also validates that new files in the symlinked directory will
// be watched, and raise events with the original path.
//
// ✅ macOS
// ❌ Linux - L493 fails because symlinks do not produce events
// ✅ Windows - requires admin permissions
func TestFileWatchSymlinkCreate(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	repoRoot := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("node_modules", "some-dep").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("parent", "child").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	// Directory layout:
	// <repoRoot>/
	//	 .git/
	//   node_modules/
	//     some-dep/
	//   parent/
	//     child/

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
		repoRoot.UntypedJoin("parent"),
		repoRoot.UntypedJoin("parent", "child"),
	}
	expectWatching(t, c, expectedWatching)

	// Create symlink during file watching
	symlinkPath := repoRoot.UntypedJoin("symlink")
	err = os.Symlink(string(repoRoot.UntypedJoin("parent", "child")), string(symlinkPath))
	assert.NilError(t, err, "Symlink")
	expectFilesystemEvent(t, ch,
		Event{
			EventType: FileAdded,
			Path:      symlinkPath,
		},
	)

	// we expect that events in the symlinked directory will be raised with the original path
	symlinkSubfile := symlinkPath.UntypedJoin("symlink_subfile")
	err = symlinkSubfile.WriteFile([]byte("hello"), 0644)
	assert.NilError(t, err, "WriteFile")
	expectFilesystemEvent(t, ch,
		Event{
			EventType: FileAdded,
			Path:      repoRoot.UntypedJoin("parent", "child", "symlink_subfile"),
		},
	)
}

// TestFileWatchSymlinkDelete tests that when a symlink is deleted,
// file watching raises no events for the virtual path
//
// ✅ macOS
// ✅ Linux
// ✅ Windows - requires admin permissions
func TestFileWatchSymlinkDelete(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	repoRoot := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("node_modules", "some-dep").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("parent", "child").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	symlinkPath := repoRoot.UntypedJoin("symlink")
	err = os.Symlink(string(repoRoot.UntypedJoin("parent", "child")), string(symlinkPath))
	assert.NilError(t, err, "Symlink")

	// Directory layout:
	// <repoRoot>/
	//	 .git/
	//   node_modules/
	//     some-dep/
	//   parent/
	//     child/
	//   symlink -> parent/child

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
		repoRoot.UntypedJoin("parent"),
		repoRoot.UntypedJoin("parent", "child"),
	}
	expectWatching(t, c, expectedWatching)

	// Delete symlink during file watching
	err = os.Remove(string(symlinkPath))
	assert.NilError(t, err, "Remove")
	expectNoFilesystemEvent(t, ch)
}

// TestFileWatchSymlinkRename tests that when a symlink is renamed,
// file watching raises a rename event for the virtual path
//
// ❌ macOS - raises no event at all
// ❌ Linux - raises an event for creating the file
// ❌ Windows - raises an event for creating the file
func TestFileWatchSymlinkRename(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	repoRoot := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("node_modules", "some-dep").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	err = repoRoot.UntypedJoin("parent", "child").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")
	symlinkPath := repoRoot.UntypedJoin("symlink")
	err = os.Symlink(string(repoRoot.UntypedJoin("parent", "child")), string(symlinkPath))
	assert.NilError(t, err, "Symlink")

	// Directory layout:
	// <repoRoot>/
	//	 .git/
	//   node_modules/
	//     some-dep/
	//   parent/
	//     child/
	//   symlink -> parent/child

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
		repoRoot.UntypedJoin("parent"),
		repoRoot.UntypedJoin("parent", "child"),
	}
	expectWatching(t, c, expectedWatching)

	// Rename symlink during file watching
	newSymlinkPath := repoRoot.UntypedJoin("new_symlink")
	err = os.Rename(string(symlinkPath), string(newSymlinkPath))
	assert.NilError(t, err, "Rename")

	expectFilesystemEvent(t, ch, Event{
		EventType: FileRenamed,
		Path:      newSymlinkPath,
	})
}

// TestFileWatchRootParentRename tests that when the parent directory of the root is renamed,
// file watching stops reporting events
//
// additionally, renmaing the root parent directory back to its original name should cause file watching
// to start reporting events again
//
// ✅ macOS
// ✅ Linux
// ✅ Windows
func TestFileWatchRootParentRename(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)

	parent := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	repoRoot := parent.UntypedJoin("repo")
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	// Directory layout:
	// <parent>/
	//   repo/
	//     .git/

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
	}
	expectWatching(t, c, expectedWatching)

	// Rename parent directory during file watching
	newRepoRoot := parent.UntypedJoin("new_repo")
	err = os.Rename(string(repoRoot), string(newRepoRoot))
	assert.NilError(t, err, "Rename")
	expectNoFilesystemEvent(t, ch)

	// Rename root parent directory back to original name
	err = os.Rename(string(newRepoRoot), string(repoRoot))
	assert.NilError(t, err, "Rename")
	expectNoFilesystemEvent(t, ch)

	// create a new file in the repo root
	err = repoRoot.UntypedJoin("new_file").WriteFile([]byte("hello"), 0666)
	assert.NilError(t, err, "WriteFile")
	expectFilesystemEvent(t, ch, Event{
		EventType: FileAdded,
		Path:      repoRoot.UntypedJoin("new_file"),
	})
}

// TestFileWatchRootParentDelete tests that when the parent directory of the root is deleted,
// file watching stops reporting events
//
// additionally, recreating the root parent directory and the root should cause file watching
// to start reporting events again
//
// ✅ macOS
// ❌ Linux - L721 no create event is emitted
// ❌ Windows - L721 no create event is emitted
func TestFileWatchRootParentDelete(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)

	parent := fs.AbsoluteSystemPathFromUpstream(t.TempDir())
	repoRoot := parent.UntypedJoin("repo")
	err := repoRoot.UntypedJoin(".git").MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	// Directory layout:
	// <parent>/
	//   repo/
	//     .git/

	watcher, err := GetPlatformSpecificBackend(logger)
	assert.NilError(t, err, "GetPlatformSpecificBackend")
	fw := New(logger, repoRoot, watcher)
	err = fw.Start()
	assert.NilError(t, err, "fw.Start")

	// Add a client
	ch := make(chan Event, 1)
	c := &testClient{
		notify: ch,
	}
	fw.AddClient(c)
	expectedWatching := []turbopath.AbsoluteSystemPath{
		repoRoot,
	}
	expectWatching(t, c, expectedWatching)

	// Delete parent directory during file watching
	err = os.RemoveAll(string(parent))
	assert.NilError(t, err, "RemoveAll")
	expectNoFilesystemEvent(t, ch)

	// create the root parent and the root again
	err = repoRoot.MkdirAll(0775)
	assert.NilError(t, err, "MkdirAll")

	expectFilesystemEvent(t, ch, Event{
		EventType: FileAdded,
		Path:      repoRoot,
	})
}
