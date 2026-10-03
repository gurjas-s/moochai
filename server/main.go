package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"tailscale.com/tsnet"
)

var (
	addr     = flag.String("addr", ":80", "address to listen on")
	hostname = flag.String("hostname", "tshello", "hostname to use in the tailnet")
)

func main() {
	flag.Parse()
	srv := new(tsnet.Server)
	srv.Hostname = *hostname
	if err := srv.Start(); err != nil {
		log.Fatalf("can't start tsnet server: %v", err)
	}
	defer srv.Close()

	ln, err := srv.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("can't listen: %v", err)
	}
	defer ln.Close()

	log.Printf("Server Running on Port %s", ln.Addr())
	log.Fatal(http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s\n", *hostname)
	})))
}

