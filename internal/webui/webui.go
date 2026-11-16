// Package webui embeds the browser-based calibration tool and serves it on a
// local HTTP server. Everything runs client-side; the server only delivers
// the static files.
package webui

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

//go:embed static
var staticFiles embed.FS

// StaticFiles returns the embedded calibration web app.
func StaticFiles() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

// Serve starts the local HTTP server and blocks until the context is
// cancelled (Ctrl+C). It returns the URL the tool was served on.
func Serve(ctx context.Context, listen string, openBrowser bool) (string, error) {
	if listen == "" {
		listen = "127.0.0.1:8484"
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.Handle("/", StaticFiles())
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := fmt.Sprintf("http://%s/", listener.Addr().String())
	fmt.Printf("Disc Image Studio calibration UI is running at %s\n", url)
	fmt.Println("Everything runs in the browser; no data leaves your machine. Press Ctrl+C to stop.")
	if openBrowser {
		tryOpenBrowser(url)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return url, nil
	case err := <-serveErr:
		if err == http.ErrServerClosed {
			return url, nil
		}
		return url, err
	}
}

func tryOpenBrowser(url string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	_ = command.Start()
}
