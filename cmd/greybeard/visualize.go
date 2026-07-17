package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"github.com/deepaksinghcs14/greybeard/internal/graph"
)

//go:embed viz.html
var vizHTML string

// cmdVisualize serves the graph as a local web page. Data is re-read from the
// store on every request, so a browser refresh shows the latest graph.
func cmdVisualize(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("visualize", flag.ExitOnError)
	port := fs.Int("port", 7333, "port to serve on (binds 127.0.0.1 only)")
	noOpen := fs.Bool("no-open", false, "don't open the browser automatically")
	fs.Parse(args)

	st, err := graph.Open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	mux := http.NewServeMux()
	// Lets the MCP server tell a live, current greybeard apart from an
	// outdated one (or an unrelated app) squatting on the port.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "greybeard "+version)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, err := st.Snapshot(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// json.Marshal escapes <, >, & — safe to inline in a <script> block.
		b, err := json.Marshal(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, strings.Replace(vizHTML, "__GREYBEARD_DATA__", string(b), 1))
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", *port)
	fmt.Printf("greybeard graph at %s (Ctrl-C to stop)\n", url)
	if !*noOpen {
		openBrowser(url)
	}
	return http.Serve(ln, mux)
}

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
	_ = cmd.Start() // best-effort; the printed URL is the fallback
}
