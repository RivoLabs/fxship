// fxship: pulls a FiveM server's resources repo, tests each push on the dev server, and lands it on
// prod once someone approves it in Discord. "fxship daemon" is the service; the other commands
// talk to it over its local API or check the box.
package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// version is set at build time (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	// systemd and the Windows service manager hand the daemon its settings already; the file is
	// root-only, so it is only read when they aren't there.
	if err := loadEnvFile(envFile()); err != nil && !os.IsNotExist(err) && !(os.IsPermission(err) && os.Getenv("API_TOKEN") != "") {
		fmt.Fprintf(os.Stderr, "reading %s: %v\n", envFile(), err)
		if os.IsPermission(err) {
			fmt.Fprintln(os.Stderr, "run it as root (sudo fxship ...), or from an elevated PowerShell on Windows")
		}
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		if !asService("fxship", daemon, nil) {
			daemon()
		}
		return
	}
	if len(os.Args) < 2 {
		os.Exit(cli([]string{"help"}))
	}
	os.Exit(cli(os.Args[1:]))
}

func daemon() {
	log.Printf("fxship %s starting", version)
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	st, err := LoadState(cfg.StatePath)
	if err != nil {
		log.Fatal(err)
	}
	a := &Agent{
		cfg:   cfg,
		git:   &Git{Dir: cfg.GitDir, URL: cfg.RepoURL, Branch: cfg.Branch},
		dev:   NewTxAdmin(cfg.Dev),
		prod:  NewTxAdmin(cfg.Prod),
		state: st,
		note:  consoleNotifier{},
	}
	if cfg.DiscordToken != "" && cfg.DiscordChannel != "" {
		d, err := NewDiscord(cfg)
		if err != nil {
			log.Fatalf("discord: %v", err)
		}
		d.agent = a
		a.note = d
	} else {
		log.Print("DISCORD_TOKEN or DISCORD_CHANNEL_ID unset: approvals via CLI only, releases logged to console")
	}

	go serveAPI(cfg, a)
	for {
		if err := a.Bootstrap(); err != nil {
			log.Printf("bootstrap: %v (retrying in 60s)", err)
			time.Sleep(60 * time.Second)
			continue
		}
		break
	}
	go a.SchedulerLoop()
	go a.CfgLoop()
	if cfg.DevEnabled {
		go a.DevWatchLoop()
	}
	a.PollLoop()
}

func serveAPI(cfg *Config, a *Agent) {
	mux := http.NewServeMux()
	auth := func(h func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(tok), []byte(cfg.APIToken)) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			out, err := h(w, r)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "release": out})
				return
			}
			json.NewEncoder(w).Encode(out)
		}
	}
	body := func(r *http.Request) map[string]string {
		m := map[string]string{}
		json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&m)
		return m
	}
	pick := func(id string) string {
		if id != "" {
			return id
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if p := a.state.Latest(StatusPending); p != nil {
			return p.ID
		}
		return ""
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /status", auth(func(w http.ResponseWriter, r *http.Request) (any, error) { return a.Snapshot(), nil }))
	mux.HandleFunc("POST /approve", auth(func(w http.ResponseWriter, r *http.Request) (any, error) {
		b := body(r)
		return a.Approve(pick(b["id"]), "server ("+b["actor"]+")")
	}))
	mux.HandleFunc("POST /reject", auth(func(w http.ResponseWriter, r *http.Request) (any, error) {
		// No id: the undecided release first, then cancel whatever is queued.
		b := body(r)
		id := pick(b["id"])
		if id == "" {
			a.mu.Lock()
			if q := a.state.Latest(StatusQueued); q != nil {
				id = q.ID
			}
			a.mu.Unlock()
		}
		return a.Reject(id, "server ("+b["actor"]+")")
	}))
	mux.HandleFunc("POST /rollback", auth(func(w http.ResponseWriter, r *http.Request) (any, error) {
		b := body(r)
		return a.Rollback(b["sha"], "server ("+b["actor"]+")")
	}))
	mux.HandleFunc("POST /apply-now", auth(func(w http.ResponseWriter, r *http.Request) (any, error) {
		return a.ApplyNow("server (" + body(r)["actor"] + ")")
	}))
	mux.HandleFunc("POST /ship", auth(func(w http.ResponseWriter, r *http.Request) (any, error) {
		b := body(r)
		e, rel, err := a.Ship(b["id"], "server ("+b["actor"]+")", b["message"])
		return map[string]any{"edit": e, "release": rel}, err
	}))
	mux.HandleFunc("POST /discard", auth(func(w http.ResponseWriter, r *http.Request) (any, error) {
		b := body(r)
		return a.Discard(b["id"], "server ("+b["actor"]+")")
	}))
	mux.HandleFunc("POST /db-refresh", auth(func(w http.ResponseWriter, r *http.Request) (any, error) {
		a.note.Info("A dev database refresh from prod was started on the server. Dev is stopped until it finishes.")
		msg, err := a.RefreshDevDB("server (" + body(r)["actor"] + ")")
		if err != nil {
			a.note.Info("Dev database refresh failed: " + err.Error())
		} else {
			a.note.Info(msg)
		}
		return map[string]string{"result": msg}, err
	}))
	mux.HandleFunc("POST /poll", auth(func(w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]string{"ok": "polled"}, a.Poll()
	}))
	log.Printf("api listening on %s", cfg.APIAddr)
	log.Fatal(http.ListenAndServe(cfg.APIAddr, mux))
}

func cli(args []string) int {
	base := "http://" + env("API_ADDR", "127.0.0.1:7070")
	who := env("SUDO_USER", env("USER", "root"))
	call := func(method, route string, payload map[string]string) int {
		if os.Getenv("API_TOKEN") == "" {
			fmt.Fprintf(os.Stderr, "no API_TOKEN: run as root (or an elevated PowerShell on Windows) so %s can be read\n", envFile())
			return 1
		}
		payload["actor"] = who
		b, _ := json.Marshal(payload)
		req, _ := http.NewRequest(method, base+route, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+os.Getenv("API_TOKEN"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\nis the agent running? systemctl status fxship\n", err)
			return 1
		}
		defer resp.Body.Close()
		var v any
		json.NewDecoder(resp.Body).Decode(&v)
		out, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(out))
		if resp.StatusCode >= 300 {
			return 1
		}
		return 0
	}
	arg := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
	}
	switch args[0] {
	case "status":
		return call("GET", "/status", map[string]string{})
	case "approve":
		return call("POST", "/approve", map[string]string{"id": arg(1)})
	case "reject", "cancel":
		return call("POST", "/reject", map[string]string{"id": arg(1)})
	case "rollback":
		if arg(1) == "" {
			fmt.Fprintln(os.Stderr, "usage: fxship rollback <sha>")
			return 2
		}
		return call("POST", "/rollback", map[string]string{"sha": arg(1)})
	case "apply-now":
		return call("POST", "/apply-now", map[string]string{})
	case "poll":
		return call("POST", "/poll", map[string]string{})
	case "ship":
		if arg(1) == "" {
			fmt.Fprintln(os.Stderr, `usage: fxship ship "what changed"`)
			return 2
		}
		return call("POST", "/ship", map[string]string{"message": strings.Join(args[1:], " ")})
	case "discard":
		return call("POST", "/discard", map[string]string{})
	case "db-refresh":
		return call("POST", "/db-refresh", map[string]string{})
	case "doctor":
		return doctor(args[1:])
	case "detect":
		return detectCmd(args[1:])
	case "upgrade":
		return upgrade(args[1:])
	case "cfg-sanitize":
		return cfgSanitizeCmd(args[1:])
	case "fxserver":
		return fxserverCmd(args[1:])
	case "version", "--version", "-v":
		fmt.Println("fxship " + version)
		return 0
	}
	fmt.Fprintln(os.Stderr, `usage: fxship <command>

Deploys (talk to the running agent):
  status              servers, deployed commits, recent releases
  approve [id]        approve the pending release (or a given one) for prod
  reject [id]         reject a pending release or cancel a queued one
  rollback <sha>      move prod back to an older commit
  apply-now           land the queued release now: 60s warning to players, then prod restarts
  poll                check GitHub for new commits now
  ship "message"      commit the edits made on dev, push them and open a prod release
  discard             put every edited file on dev back to what GitHub has
  db-refresh          replace the dev database with a copy of prod's

The box:
  doctor              check every piece of the setup and say how to fix what's wrong
  detect <dir>        show the framework, core and streaming resources fxship sees in a folder
  upgrade [--check]   update fxship to the latest release
  version             print the version
  daemon              run the agent (systemd does this)`)
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return 0
	}
	return 2
}

func cfgSanitizeCmd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: fxship cfg-sanitize <server-data dir>")
		return 2
	}
	if _, err := SyncCfg(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
