package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type LintIssue struct {
	Path string `json:"path"`
	Msg  string `json:"msg"`
}

// CfxLua adds syntax stock Lua 5.4 rejects. Rewrite it to plain Lua before luac sees it, so the
// check only flags real syntax errors. Line numbers are preserved.
var (
	reBacktick = regexp.MustCompile("`[^`\n]*`")
	reCompound = regexp.MustCompile(`([A-Za-z_][\w.:\[\]"']*)\s*(\+|-|\*|/|<<|>>|&|\||\^)=([^=])`)
	reSafeNav  = regexp.MustCompile(`\?([.\[:])`)
	reLocalIn  = regexp.MustCompile(`(?m)^(\s*local\s+[A-Za-z_][\w\s,]*?)\s+in\s+`)
)

func cfxToLua(src []byte) []byte {
	s := reBacktick.ReplaceAll(src, []byte("0"))
	s = reCompound.ReplaceAll(s, []byte("$1 = $1 $2$3"))
	s = reSafeNav.ReplaceAll(s, []byte("$1"))
	s = reLocalIn.ReplaceAll(s, []byte("$1 = "))
	return s
}

// luacPath is the Lua 5.4 compiler used for syntax checks: luac5.4 from apt on Linux, the luac.exe
// the installer puts next to fxship.exe on Windows. LUAC overrides it.
func luacPath() string {
	if v := env("LUAC", ""); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if self, err := os.Executable(); err == nil {
			if p := filepath.Join(filepath.Dir(self), "luac.exe"); fileExists(p) {
				return p
			}
		}
		return "luac.exe"
	}
	return "luac5.4"
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func lintFile(p string, src []byte) *LintIssue {
	switch strings.ToLower(path.Ext(p)) {
	case ".lua":
		cmd := exec.Command(luacPath(), "-p", "-")
		cmd.Stdin = bytes.NewReader(cfxToLua(src))
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(errb.String())
			// "luac5.4: stdin:3: ..." on Linux, "C:\...\luac.exe: stdin:3: ..." on Windows.
			if i := strings.Index(msg, "stdin:"); i >= 0 {
				msg = msg[i+len("stdin:"):]
			}
			if msg == "" {
				msg = err.Error()
			}
			return &LintIssue{Path: p, Msg: "line " + msg}
		}
	case ".json":
		if len(bytes.TrimSpace(src)) > 0 && !json.Valid(src) {
			var v any
			err := json.Unmarshal(src, &v)
			return &LintIssue{Path: p, Msg: fmt.Sprintf("invalid JSON: %v", err)}
		}
	}
	return nil
}

// Lint checks the Lua and JSON files that changed. Escrowed (encrypted) files are skipped.
func Lint(g *Git, sha string, files []FileChange) []LintIssue {
	var issues []LintIssue
	for _, f := range files {
		if f.Status == "D" {
			continue
		}
		ext := strings.ToLower(path.Ext(f.Path))
		if ext != ".lua" && ext != ".json" {
			continue
		}
		src, err := g.Show(sha, f.Path)
		if err != nil {
			issues = append(issues, LintIssue{Path: f.Path, Msg: "could not read: " + err.Error()})
			continue
		}
		if ext == ".lua" && isEscrowed(src) {
			continue
		}
		if is := lintFile(f.Path, src); is != nil {
			issues = append(issues, *is)
		}
	}
	return issues
}

func isEscrowed(src []byte) bool {
	head := src
	if len(head) > 64 {
		head = head[:64]
	}
	return bytes.HasPrefix(head, []byte("FXAP")) || bytes.Contains(head, []byte("\x00"))
}
