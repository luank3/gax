package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"
	"syscall"
	"time"
)

const pruneInterval = 24 * time.Hour

var pruneProcess struct {
	sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// RetentionCutoff returns the inactivity cutoff used by session listing and pruning.
func RetentionCutoff(retentionDays int) time.Time {
	if retentionDays <= 0 {
		return time.Time{}
	}
	return time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
}

// Touch refreshes a selected session's modification time while attempting a shared lock, so
// another process's pruner sees the activity before it considers unlinking the file.
func Touch(path string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return errors.New("cannot open session file")
	}
	defer file.Close()
	_ = syscall.Flock(fd, syscall.LOCK_SH)
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !hasLinks(info) {
		return errors.New("session path is not a linked regular file")
	}
	now := time.Now()
	timestamp := syscall.NsecToTimeval(now.UnixNano())
	return syscall.Futimes(fd, []syscall.Timeval{timestamp, timestamp})
}

// PruneBefore synchronously removes expired session files across all cwd buckets. It leaves
// nonstandard names, symlinks, active writers, and the optional resumed path untouched.
func PruneBefore(cutoff time.Time, excludePath string) error {
	if cutoff.IsZero() {
		return nil
	}
	return pruneTree(context.Background(), cutoff, excludePath)
}

// StartPrune starts at most one background sweep per process and uses a locked marker to throttle
// sweeps across launches to once per day. A zero-length marker records an incomplete prior run.
func StartPrune(retentionDays int, excludePath string) {
	cutoff := RetentionCutoff(retentionDays)
	if cutoff.IsZero() {
		return
	}
	pruneProcess.Lock()
	defer pruneProcess.Unlock()
	if pruneProcess.done != nil {
		return
	}
	sessionsRoot, err := sessionsRootPath()
	if err != nil {
		return
	}
	root, rootInfo, err := openDirectoryRoot(sessionsRoot)
	if err != nil {
		return
	}
	marker, err := openPruneMarker(root)
	if err != nil {
		_ = root.Close()
		return
	}
	if err := syscall.Flock(int(marker.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = marker.Close()
		_ = root.Close()
		return
	}
	markerInfo, err := marker.Stat()
	if err != nil {
		_ = syscall.Flock(int(marker.Fd()), syscall.LOCK_UN)
		_ = marker.Close()
		_ = root.Close()
		return
	}
	now := time.Now()
	if markerInfo.Size() > 0 && now.Sub(markerInfo.ModTime()) < pruneInterval {
		_ = syscall.Flock(int(marker.Fd()), syscall.LOCK_UN)
		_ = marker.Close()
		_ = root.Close()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	pruneProcess.cancel = cancel
	pruneProcess.done = done
	go func() {
		defer close(done)
		defer root.Close()
		defer marker.Close()
		if err := pruneTreeAt(ctx, root, rootInfo, cutoff, excludedFileInfo(excludePath)); err != nil || ctx.Err() != nil {
			return
		}
		if marker.Truncate(0) != nil {
			return
		}
		if _, err := marker.Seek(0, 0); err != nil {
			return
		}
		_, _ = marker.Write([]byte("1"))
	}()
}

// ShutdownPrune cancels and joins the process's background session sweep.
func ShutdownPrune() {
	pruneProcess.Lock()
	cancel, done := pruneProcess.cancel, pruneProcess.done
	pruneProcess.Unlock()
	if done == nil {
		return
	}
	if cancel != nil {
		cancel()
	}
	<-done
	pruneProcess.Lock()
	if pruneProcess.done == done {
		pruneProcess.cancel = nil
		pruneProcess.done = nil
	}
	pruneProcess.Unlock()
}

func sessionsRootPath() (string, error) {
	return statePath("sessions")
}

func openDirectoryRoot(path string) (*os.Root, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("session path is not a real directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		if err == nil {
			err = errors.New("session directory changed while opening")
		}
		return nil, nil, err
	}
	return root, opened, nil
}

func openPruneMarker(root *os.Root) (*os.File, error) {
	info, err := root.Lstat(".prune")
	if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("prune marker is not a regular file")
	}
	flags := os.O_RDWR
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil {
		return nil, err
	}
	file, err := root.OpenFile(".prune", flags, 0o600)
	if errors.Is(err, os.ErrExist) {
		file, err = root.OpenFile(".prune", os.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	current, currentErr := root.Lstat(".prune")
	if statErr != nil || currentErr != nil || !opened.Mode().IsRegular() || !os.SameFile(opened, current) {
		_ = file.Close()
		if statErr != nil {
			return nil, statErr
		}
		if currentErr != nil {
			return nil, currentErr
		}
		return nil, errors.New("prune marker changed while opening")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func pruneTree(ctx context.Context, cutoff time.Time, excludePath string) error {
	sessionsRoot, err := sessionsRootPath()
	if err != nil {
		return nil
	}
	root, rootInfo, err := openDirectoryRoot(sessionsRoot)
	if err != nil {
		return nil
	}
	defer root.Close()
	return pruneTreeAt(ctx, root, rootInfo, cutoff, excludedFileInfo(excludePath))
}

// excludedFileInfo resolves the path a sweep must leave alone. The sweep compares file identity
// rather than path strings: entries under an *os.Root are named relative to that root, while the
// caller holds an absolute path, so the two strings differ for the same file.
func excludedFileInfo(excludePath string) os.FileInfo {
	if excludePath == "" {
		return nil
	}
	info, err := os.Lstat(excludePath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return info
}

func pruneTreeAt(ctx context.Context, root *os.Root, rootInfo os.FileInfo, cutoff time.Time, excluded os.FileInfo) error {
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(rootInfo, opened) {
		return nil
	}
	projects, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := project.Name()
		if name == "." || name == ".." {
			continue
		}
		projectInfo, err := root.Lstat(name)
		if err != nil || !projectInfo.IsDir() || projectInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		projectRoot, err := root.OpenRoot(name)
		if err != nil {
			continue
		}
		openedProject, statErr := projectRoot.Stat(".")
		if statErr != nil || !os.SameFile(projectInfo, openedProject) {
			_ = projectRoot.Close()
			continue
		}
		entries, readErr := fs.ReadDir(projectRoot.FS(), ".")
		if readErr == nil {
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					_ = projectRoot.Close()
					return err
				}
				pruneSessionFile(projectRoot, entry.Name(), cutoff, excluded)
			}
		}
		_ = projectRoot.Close()
		currentProject, err := root.Lstat(name)
		if err == nil && currentProject.IsDir() && currentProject.Mode()&os.ModeSymlink == 0 && os.SameFile(projectInfo, currentProject) {
			_ = root.Remove(name) // only empty buckets are removed
		}
	}
	return nil
}

func pruneSessionFile(projectRoot *os.Root, name string, cutoff time.Time, excluded os.FileInfo) {
	if !PathIsStandard(name) {
		return
	}
	entry, err := projectRoot.Lstat(name)
	if err != nil || !entry.Mode().IsRegular() || entry.Mode()&os.ModeSymlink != 0 || !entry.ModTime().Before(cutoff) {
		return
	}
	if excluded != nil && os.SameFile(entry, excluded) {
		return
	}
	file, err := projectRoot.Open(name)
	if err != nil {
		return
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return
	}
	locked, err := file.Stat()
	if err != nil || !locked.Mode().IsRegular() || !hasLinks(locked) ||
		!locked.ModTime().Before(cutoff) || !os.SameFile(entry, locked) {
		return
	}
	current, err := projectRoot.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(locked, current) {
		return
	}
	if excluded != nil && os.SameFile(current, excluded) {
		return
	}
	_ = projectRoot.Remove(name)
}

func hasLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 0
}
