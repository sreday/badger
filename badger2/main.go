// Badger 2.0 on a laptop: the badge printer, friendlier. Runs beside the
// original Badger (the root of this repo), which stays as it is. The app
// itself is in app/, which Snout also hosts at /badger.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"

	"github.com/sreday/badger/badger2/app"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("badger2", version)
		return
	}
	log.Printf("Badger 2.0 %s starting", version)

	cfg := app.LoadConfig()

	srv, err := app.NewServer(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}

	addr := cfg.Host + ":" + cfg.Port
	browserURL := "http://localhost:" + cfg.Port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Already running (typed "badger2" twice): show it instead.
		log.Printf("Badger 2.0 is already running: opening %s", browserURL)
		openBrowser(browserURL)
		return
	}
	log.Printf("Badger 2.0 is running at %s (close this window to stop it)", browserURL)
	if os.Getenv("BADGER_NO_BROWSER") == "" {
		openBrowser(browserURL)
	}
	if err := http.Serve(ln, srv.Handler()); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// openBrowser opens a URL in the default browser, best effort.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Open %s in your browser", url)
	}
}
