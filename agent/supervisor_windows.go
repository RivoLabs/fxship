//go:build windows

package main

// On Windows there is no systemd, so "fxship fxserver <prod|dev>" is the service that runs each
// FXServer. It starts FXServer.exe (which hosts txAdmin, which runs the game server as a child),
// restarts it if it dies, and puts the whole tree in a Job Object: the Windows counterpart of the
// systemd limits on Linux, so dev can never starve prod of CPU or memory.

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobLimits mirrors host/dev.conf and host/prod.conf.
type jobLimits struct {
	cpuCores  float64 // hard cap in cores, 0 for none
	memoryMax uint64  // bytes for the whole tree, 0 for none
	priority  uint32  // process priority class
}

// limitsFor reads FX_CPU_CORES and FX_MEMORY_MAX_MB from the server's env file (the installer writes
// them for dev from DEV_CPU_CORES and DEV_MEMORY_MAX_MB).
func limitsFor(name string, settings []string) jobLimits {
	l := jobLimits{priority: windows.ABOVE_NORMAL_PRIORITY_CLASS}
	if name == "dev" {
		l = jobLimits{cpuCores: 2, memoryMax: 4 << 30, priority: windows.BELOW_NORMAL_PRIORITY_CLASS}
	}
	for _, kv := range settings {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "FX_CPU_CORES":
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				l.cpuCores = f
			}
		case "FX_MEMORY_MAX_MB":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				l.memoryMax = n << 20
			}
		}
	}
	return l
}

// JOBOBJECT_CPU_RATE_CONTROL_INFORMATION; x/sys doesn't define it.
type cpuRateControl struct {
	ControlFlags uint32
	CpuRate      uint32
}

const (
	jobObjectCpuRateControlInformation = 15
	cpuRateControlEnable               = 0x1
	cpuRateControlHardCap              = 0x4
)

// newJob makes a job object with the limits and puts this process in it, so FXServer and every
// process it starts inherit them, and all of them die if this service does.
func newJob(l jobLimits) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS
	info.BasicLimitInformation.PriorityClass = l.priority
	if l.memoryMax > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(l.memoryMax)
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, fmt.Errorf("job limits: %w", err)
	}
	if l.cpuCores > 0 {
		rate := uint32(l.cpuCores / float64(runtime.NumCPU()) * 10000)
		if rate > 0 && rate < 10000 {
			cr := cpuRateControl{ControlFlags: cpuRateControlEnable | cpuRateControlHardCap, CpuRate: rate}
			if _, err := windows.SetInformationJobObject(job, jobObjectCpuRateControlInformation,
				uintptr(unsafe.Pointer(&cr)), uint32(unsafe.Sizeof(cr))); err != nil {
				return 0, fmt.Errorf("cpu cap: %w", err)
			}
		}
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		return 0, fmt.Errorf("joining the job: %w", err)
	}
	return job, nil
}

// readEnv reads KEY=value lines (the txAdmin TXHOST_* settings for one server).
func readEnv(p string) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" && !strings.HasPrefix(l, "#") && strings.Contains(l, "=") {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}

func fxserverCmd(args []string) int {
	if len(args) != 1 || (args[0] != "prod" && args[0] != "dev") {
		fmt.Fprintln(os.Stderr, "usage: fxship fxserver <prod|dev>   (run by the fxship-prod and fxship-dev services)")
		return 2
	}
	name := args[0]
	var (
		mu      sync.Mutex
		cur     *exec.Cmd
		stopped bool
	)
	stop := func() {
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		if cur != nil && cur.Process != nil {
			// FXServer has no console to send Ctrl+C to under a service; end the whole tree.
			exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cur.Process.Pid)).Run()
		}
	}
	run := func() {
		svcName := "fxship-" + name
		root := env("FIVEM_ROOT", paths().FivemRoot)
		envPath := filepath.Join(filepath.Dir(paths().EnvFile), name+".env")
		settings, _ := readEnv(envPath)
		if _, err := newJob(limitsFor(name, settings)); err != nil {
			log.Printf("%s: resource limits not applied: %v", svcName, err)
		}
		out := &rotating{path: filepath.Join(serviceLogDir(svcName), svcName+"-console.log"), max: 50 << 20}
		for {
			mu.Lock()
			if stopped {
				mu.Unlock()
				return
			}
			extra, err := readEnv(envPath)
			if err != nil {
				mu.Unlock()
				log.Printf("%s: %v (retrying in 30s)", svcName, err)
				time.Sleep(30 * time.Second)
				continue
			}
			// The installer names the build in the env file; it's re-read before every start.
			fxDir := filepath.Join(root, "artifacts", "current")
			for _, kv := range extra {
				if k, v, _ := strings.Cut(kv, "="); k == "FXSERVER_DIR" && v != "" {
					fxDir = v
				}
			}
			cmd := exec.Command(filepath.Join(fxDir, "FXServer.exe"))
			cmd.Dir = filepath.Join(root, name)
			cmd.Env = append(os.Environ(), extra...)
			cmd.Stdout, cmd.Stderr = out, out
			err = cmd.Start()
			cur = cmd
			mu.Unlock()
			if err != nil {
				log.Printf("%s: starting FXServer: %v (retrying in 30s)", svcName, err)
				time.Sleep(30 * time.Second)
				continue
			}
			log.Printf("%s: FXServer started, pid %d", svcName, cmd.Process.Pid)
			err = cmd.Wait()
			mu.Lock()
			done := stopped
			mu.Unlock()
			if done {
				return
			}
			log.Printf("%s: FXServer exited (%v), restarting in 10s", svcName, err)
			time.Sleep(10 * time.Second)
		}
	}
	if asService("fxship-"+name, run, stop) {
		return 0
	}
	run() // in a console, for debugging
	return 0
}
