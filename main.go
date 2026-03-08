package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/ty802/copilot-proxy/auth"
	"github.com/ty802/copilot-proxy/proxy"
)

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	forceLogin := flag.Bool("login", false, "Force a new GitHub device flow login")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("copilot-proxy ")

	fmt.Print(`
  ┌─────────────────────────────────────────────────────────┐
  │           copilot-proxy — Claude Code ↔ GitHub Copilot  │
  └─────────────────────────────────────────────────────────┘
`)

	mgr, err := auth.NewManager(*forceLogin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth error: %v\n", err)
		os.Exit(1)
	}

	if err := mgr.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "token validation failed: %v\n", err)
		os.Exit(1)
	}

	handler := proxy.NewHandler(mgr.Token, mgr.RefreshToken)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("Listening on %s", addr)
	fmt.Printf(`
  Ready. Point Claude Code at this proxy:

    export ANTHROPIC_BASE_URL=http://localhost:%d
    export ANTHROPIC_API_KEY=dummy

  Then run:  claude

`, *port)

	srv := &http.Server{Addr: addr, Handler: handler}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
