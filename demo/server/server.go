// Package server provides a sample HTTP/Websocket server which registers
// itself in consul using one or more url prefixes to demonstrate and
// test the automatic fabio routing table update.
//
// During startup the server performs the following steps:
//
// * Add a handler for each prefix which provides a unique
//   response for that instance and endpoint
// * Add a `/health` handler for the consul health check
// * Register the service in consul with the listen address,
//   a health check under the given name and with one `urlprefix-`
//   tag per prefix
// * Install a signal handler to deregister the service on exit
//
// If the protocol is set to "ws" the registered endpoints function
// as websocket echo servers.
//
// Example:
//
//   # http server
//   ./server -addr 127.0.0.1:5000 -name svc-a -prefix /foo,/bar
//   ./server -addr 127.0.0.1:5001 -name svc-b -prefix /baz,/bar
//
//   # websocket server
//   ./server -addr 127.0.0.1:6000 -name ws-a -prefix /echo1,/echo2 -proto ws
//
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"math/rand"

	"github.com/hashicorp/consul/api"
	"golang.org/x/net/websocket"
)

func main() {
	var addr, consul, name, prefix, proto, token string
	var response_size int
	var sleep_delay_ms int64
	flag.StringVar(&addr, "addr", "127.0.0.1:5000", "host:port of the service")
	flag.StringVar(&consul, "consul", "127.0.0.1:8500", "host:port of the consul agent")
	flag.StringVar(&name, "name", filepath.Base(os.Args[0]), "name of the service")
	flag.StringVar(&prefix, "prefix", "", "comma-sep list of host/path prefixes to register")
	flag.StringVar(&proto, "proto", "http", "protocol for endpoints: http or ws")
	flag.StringVar(&token, "token", "", "consul ACL token")
	flag.IntVar(&response_size, "response_size", 0, "Desired response size")
	flag.Int64Var(&sleep_delay_ms, "sleep_delay_ms", 0, "Maximum number of milliseconds to sleep before sending response")
	flag.Parse()

	if prefix == "" {
		flag.Usage()
		os.Exit(1)
	}

	var response_str string

	if response_size > 0 {
		response_str = strings.Repeat(".", response_size)
	}

	// register prefixes
	prefixes := strings.Split(prefix, ",")
	for _, p := range prefixes {
		switch proto {
		case "http":
			if response_size > 0 {
				http.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
					if sleep_delay_ms > 0 {
						duration := rand.Int63n(sleep_delay_ms)
						log.Printf("Sleeping for %d ms", duration)
						time.Sleep(time.Duration(duration) * time.Millisecond)
					}
					fmt.Fprintf(w, "%s\n", response_str)
				})

			} else {
				http.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprintf(w, "Serving %s from %s on %s\n", r.RequestURI, name, addr)
				})
			}
		case "ws":
			http.Handle(p, websocket.Handler(EchoServer))
		default:
			log.Fatal("Invalid protocol ", proto)
		}
	}

	// start http server
	go func() {
		log.Printf("Listening on %s serving %s", addr, prefix)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatal(err)
		}
	}()

	// register consul health check endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "OK")
	})

	// build urlprefix-host/path tag list
	// e.g. urlprefix-/foo, urlprefix-/bar, ...
	var tags []string
	for _, p := range prefixes {
		tags = append(tags, "urlprefix-"+p)
	}

	// get host and port as string/int
	host, portstr, err := net.SplitHostPort(addr)
	if err != nil {
		log.Fatal(err)
	}
	port, err := strconv.Atoi(portstr)
	if err != nil {
		log.Fatal(err)
	}

	// register service with health check
	serviceID := name + "-" + addr
	service := &api.AgentServiceRegistration{
		ID:      serviceID,
		Name:    name,
		Port:    port,
		Address: host,
		Tags:    tags,
		Check: &api.AgentServiceCheck{
			HTTP:     "http://" + addr + "/health",
			Interval: "1s",
			Timeout:  "1s",
		},
	}

	config := &api.Config{Address: consul, Scheme: "http", Token: token}
	client, err := api.NewClient(config)
	if err != nil {
		log.Fatal(err)
	}

	if err := client.Agent().ServiceRegister(service); err != nil {
		log.Fatal(err)
	}
	log.Printf("Registered service %q in consul with tags %q", name, strings.Join(tags, ","))

	// run until we get a signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, os.Kill)
	<-quit

	// deregister service
	if err := client.Agent().ServiceDeregister(serviceID); err != nil {
		log.Fatal(err)
	}
	log.Printf("Deregistered service %q in consul", name)
}

func EchoServer(ws *websocket.Conn) {
	addr := ws.LocalAddr().String()
	pfx := []byte("[" + addr + "] ")

	log.Printf("ws connect on %s", addr)

	// the following could be done with io.Copy(ws, ws)
	// but I want to add some meta data
	var msg = make([]byte, 1024)
	for {
		n, err := ws.Read(msg)
		if err != nil && err != io.EOF {
			log.Printf("ws error on %s. %s", addr, err)
			break
		}
		_, err = ws.Write(append(pfx, msg[:n]...))
		if err != nil && err != io.EOF {
			log.Printf("ws error on %s. %s", addr, err)
			break
		}
	}
	log.Printf("ws disconnect on %s", addr)
}
