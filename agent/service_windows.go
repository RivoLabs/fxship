//go:build windows

package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows/svc"
)

// asService runs work under the Windows service manager when started by it, and reports whether it
// did. A stop request ends the process: state is saved atomically after every change, the same
// guarantee the agent relies on when systemd stops it.
func asService(name string, work func(), onStop func()) bool {
	is, err := svc.IsWindowsService()
	if err != nil || !is {
		return false
	}
	logToFile(name)
	if err := svc.Run(name, &service{work: work, onStop: onStop}); err != nil {
		log.Fatalf("service %s: %v", name, err)
	}
	return true
}

type service struct {
	work   func()
	onStop func()
}

func (s *service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	go s.work()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			if s.onStop != nil {
				s.onStop()
			}
			return false, 0
		}
	}
	return false, 0
}

// logDir is where Windows services write the logs journald keeps on Linux.
func logDir() string { return filepath.Join(filepath.Dir(paths().EnvFile), "logs") }

// serviceLogDir: each game server writes into its own folder, which only its account may change, so
// dev can't read or wipe prod's console. The agent and backups write to logs\ itself.
func serviceLogDir(name string) string {
	if name == "fxship-prod" || name == "fxship-dev" {
		return filepath.Join(logDir(), name)
	}
	return logDir()
}

// logToFile points the standard logger at <serviceLogDir>\<name>.log, keeping one older file of 20 MB.
func logToFile(name string) {
	os.MkdirAll(serviceLogDir(name), 0o755)
	log.SetOutput(&rotating{path: filepath.Join(serviceLogDir(name), name+".log"), max: 20 << 20})
}

// rotating is a log file that moves itself to .1 once it passes max bytes.
type rotating struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func (r *rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil || r.size+int64(len(p)) > r.max {
		if r.f != nil {
			r.f.Close()
			os.Rename(r.path, r.path+".1")
		}
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		fi, _ := f.Stat()
		r.f, r.size = f, fi.Size()
		if r.size > r.max {
			f.Close()
			os.Rename(r.path, r.path+".1")
			if r.f, err = os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err != nil {
				return 0, err
			}
			r.size = 0
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

var _ io.Writer = (*rotating)(nil)
