package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server"
)

func init() {
	register(Scenario{
		ID:      "20-port-in-use-crash",
		Title:   "Server reports it started and then dies when the port is already in use",
		Timeout: 120 * time.Second,
		Child:   true,
		Bug: "`Config.New` logs a listener error and then appends the **nil** listener anyway:\n\n" +
			"```go\n" +
			"for _, lf := range conf.Listeners {\n" +
			"    l, err := lf(conf)\n" +
			"    if err != nil {\n" +
			"        conf.Log.Error(\"create listener: \" + err.Error())\n" +
			"    }\n" +
			"    srv.listeners = append(srv.listeners, l)   // l is nil here\n" +
			"}\n```\n\n" +
			"`Server.Listen` then logs `\"Dragonfly server started.\"` and calls `startListening`, which\n" +
			"launches `go srv.listen(l)` on the nil listener. The first `l.Accept()` is a nil pointer\n" +
			"dereference on a background goroutine and the whole process dies - after having told the\n" +
			"operator that the server started successfully.",
		Run: runPortInUse,
		OnChildCrash: func(partial, stderr string, err error) Result {
			combined := partial + stderr
			startedLogged := strings.Contains(combined, "Dragonfly server started.")
			listenerErr := strings.Contains(combined, "create listener")
			crashed := strings.Contains(combined, "panic:")
			res := Result{
				Setup: "A UDP socket is bound to a free port on 127.0.0.1 first. A standard dragonfly server is\n" +
					"then built from `server.DefaultConfig()` -> `UserConfig.Config(log)` (the same path\n" +
					"`main.go` in this repository uses, including the stock `UserConfig.listenerFunc`) and told\n" +
					"to listen on that same address.",
				ServerSteps: []string{
					"bound a UDP socket on 127.0.0.1 to occupy the port",
					"built the server with the stock `server.DefaultConfig()` / `UserConfig.Config(log)` path and called `(*Server).Listen()`",
					"the server log is written to stdout so the parent process can read it",
				},
				ClientSteps: []string{
					"none - no client is involved, the server dies before anything can connect",
				},
				Observed: partial + "\n--- child process output ---\n" + truncate(stderr, 6000) +
					fmt.Sprintf("\n--- child exit: %v ---", err),
				Expected: "`Config.New` should not register a nil listener, and `Listen` should report the failure\n" +
					"instead of announcing a successful start and then crashing.",
			}
			switch {
			case startedLogged && crashed:
				res.Verdict = Reproduced
				res.Summary = "the log says \"Dragonfly server started.\" and the process then panics on the nil listener"
			case crashed:
				res.Verdict = Reproduced
				res.Summary = "the server process panicked after failing to bind the port"
			default:
				res.Verdict = NotReproduced
				res.Summary = fmt.Sprintf("started-logged=%v listener-error=%v crashed=%v", startedLogged, listenerErr, crashed)
			}
			return res
		},
	})
}

func runPortInUse() Result {
	var o out
	res := Result{
		Setup: "A UDP socket is bound to a free port on 127.0.0.1 first. A standard dragonfly server is then\n" +
			"built from `server.DefaultConfig()` -> `UserConfig.Config(log)` (the same path `main.go` in\n" +
			"this repository uses, including the stock `UserConfig.listenerFunc`) and told to listen on that\n" +
			"same address.",
		ServerSteps: []string{
			"bound a UDP socket on 127.0.0.1 to occupy the port",
			"built the server with the stock `server.DefaultConfig()` / `UserConfig.Config(log)` path and called `(*Server).Listen()`",
		},
		ClientSteps: []string{"none - no client is involved, the server dies before anything can connect"},
	}

	port, err := freePort()
	if err != nil {
		return blocked(res, "could not find a free port: "+err.Error())
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return blocked(res, err.Error())
	}
	blocker, err := net.ListenUDP("udp", a)
	if err != nil {
		return blocked(res, "could not occupy the port: "+err.Error())
	}
	defer blocker.Close()
	o.printf("occupied %s with a plain UDP socket", addr)

	dir, err := os.MkdirTemp("", "bugrepro-port-")
	if err != nil {
		return blocked(res, err.Error())
	}
	defer os.RemoveAll(dir)

	uc := server.DefaultConfig()
	uc.Network.Address = addr
	uc.Server.AuthEnabled = false
	uc.World.SaveData = false
	uc.Players.SaveData = false
	uc.Resources.AutoBuildPack = false
	uc.Resources.Folder = filepath.Join(dir, "resources")

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	conf, err := uc.Config(log)
	if err != nil {
		return blocked(res, "building the config failed: "+err.Error())
	}
	o.printf("built a stock dragonfly config via server.DefaultConfig() -> UserConfig.Config(log)")
	o.printf("calling conf.New() and then (*Server).Listen() on the occupied address...")

	srv := conf.New()
	srv.Listen()

	// Give the listening goroutine a moment to touch the nil listener.
	time.Sleep(5 * time.Second)

	o.printf("the process is still alive 5 seconds after Listen()")
	_ = srv.Close()

	res.Observed = o.String()
	res.Expected = "`Config.New` should not register a nil listener, and `Listen` should report the failure\n" +
		"instead of announcing a successful start."
	res.Verdict = NotReproduced
	res.Summary = "the server survived starting on an occupied port"
	return res
}
