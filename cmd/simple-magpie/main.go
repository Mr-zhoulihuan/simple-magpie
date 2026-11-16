// simple-magpie: a small local LLM gateway in the spirit of magpie — it routes
// every agent's request to a provider/account, relays or translates between
// APIs, and shows what each prompt held on a Context Window dashboard.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"simple-magpie/internal/config"
	"simple-magpie/internal/gateway"
	"simple-magpie/internal/store"
	"simple-magpie/internal/web"
)

func main() {
	cfgPath := flag.String("config", "config/config.json", "path to the config file")
	addr := flag.String("listen", "", "listen address (overrides config)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	listen := cfg.Listen
	if *addr != "" {
		listen = *addr
	}

	st := store.New(500)
	gw := gateway.New(cfg, st)
	dash := web.New(st)

	mux := http.NewServeMux()
	// dashboard
	mux.Handle("/dashboard", dash.Handler())
	mux.Handle("/dashboard/", dash.Handler())
	// everything else is the gateway
	mux.Handle("/", gw.Handler())

	log.Printf("simple-magpie listening on http://%s", listen)
	log.Printf("  gateway:   http://%s/v1  (token: %s)", listen, gateway.Token)
	log.Printf("  dashboard: http://%s/dashboard", listen)
	log.Printf("  providers: %v", cfg.ProviderIDs())
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Printf("server: %v", err)
		os.Exit(1)
	}
}
