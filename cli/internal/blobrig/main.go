package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ocelhq/ocel/cli/internal/devserver"
)

func main() {
	apiURL := flag.String("api", "", "Ocel API base URL")
	token := flag.String("token", "", "leader access token")
	projectID := flag.String("project", "", "project id")
	flag.Parse()

	if *apiURL == "" || *projectID == "" {
		fmt.Fprintln(os.Stderr, "blobrig: -api and -project are required")
		os.Exit(2)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "blobrig: listen:", err)
		os.Exit(1)
	}
	devServerAddr := "http://" + listener.Addr().String()

	srv := devserver.New(*apiURL, *token, *projectID, devServerAddr)
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go srv.RunDetector(ctx, func(err error) {
		fmt.Fprintln(os.Stderr, "blobrig detect:", err)
	})

	fmt.Printf("RIG_ADDR=%s\n", devServerAddr)

	go func() {
		io.Copy(io.Discard, os.Stdin)
		stop()
	}()
	<-ctx.Done()
}
