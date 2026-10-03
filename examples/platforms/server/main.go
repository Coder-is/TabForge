package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Coder-is/TabForge/internal/platformtest"
	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/protocol/httptransport"
)

func main() {
	address := flag.String("listen", "127.0.0.1:18083", "local acceptance server address")
	manifest := flag.String("contract", "examples/protocol/contract.json", "chat acceptance contract")
	assets := flag.String("assets", "examples/platforms", "browser acceptance assets")
	flag.Parse()
	c, err := protocol.Load(*manifest)
	if err != nil {
		log.Fatal(err)
	}
	fixture, err := platformtest.New(c)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", fixture)
	mux.Handle("/_platform/", fixture)
	mux.Handle("/", http.FileServer(http.Dir(*assets)))
	server := httptransport.HTTPServer(*address, mux)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		stop, release := context.WithTimeout(context.Background(), 5*time.Second)
		defer release()
		if server.Shutdown(stop) != nil {
			server.Close()
		}
	}()
	log.Printf("Acceptance server http://%s/browser.html (deliberate faults; local testing only)", *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
