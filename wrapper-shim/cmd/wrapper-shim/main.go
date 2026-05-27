// Command wrapper-shim is a translating proxy that lets clients of the
// original WorldObservationLog/wrapper raw-TCP protocol talk to a
// WorldObservationLog/wrapper-manager gRPC backend without code changes.
//
// It opens two TCP listeners that mimic the wrapper byte-for-byte:
//
//	-m3u8-listen     defaults to :20020 (wrapper's get-m3u8 port)
//	-decrypt-listen  defaults to :10020 (wrapper's decrypt port)
//
// Each accepted connection is translated into the appropriate gRPC call(s)
// against -manager (defaults to 127.0.0.1:8080).
//
// Typical deployment: one shim instance next to each wrapper-manager, with
// the bot configured to point at the shim's listen addresses (the same
// values it used to point at when running plain wrapper).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/wrapper-shim/internal/shim"
)

func main() {
	manager := flag.String("manager", "127.0.0.1:8080",
		"address of wrapper-manager gRPC server")
	m3u8Listen := flag.String("m3u8-listen", ":20020",
		"TCP listen address for the M3U8 (get-m3u8) shim")
	decryptListen := flag.String("decrypt-listen", ":10020",
		"TCP listen address for the Decrypt (decrypt-m3u8) shim")
	waitReady := flag.Duration("wait-ready", 60*time.Second,
		"how long to wait for wrapper-manager to report Ready=true on startup; "+
			"set to 0 to skip the readiness probe")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stdout)

	rootCtx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := shim.NewClient(*manager)
	if err != nil {
		log.Fatalf("connect wrapper-manager: %v", err)
	}
	defer client.Close()

	if *waitReady > 0 {
		if err := client.WaitReady(rootCtx, *waitReady); err != nil {
			// Non-fatal: bot connections will fail with a clean error
			// until the manager finishes warming up. We still bring the
			// listeners up so the system isn't blocked on a single slow
			// Apple ID restore.
			log.Printf("warning: %v; starting listeners anyway", err)
		}
	}

	m3u8Server := shim.NewM3U8Server(*m3u8Listen, client)
	decryptServer := shim.NewDecryptServer(*decryptListen, client)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := m3u8Server.ListenAndServe(rootCtx); err != nil {
			log.Printf("m3u8 server stopped: %v", err)
			stop()
		}
	}()

	go func() {
		defer wg.Done()
		if err := decryptServer.ListenAndServe(rootCtx); err != nil {
			log.Printf("decrypt server stopped: %v", err)
			stop()
		}
	}()

	<-rootCtx.Done()
	log.Printf("shutdown signal received; waiting for listeners...")
	wg.Wait()
	log.Printf("wrapper-shim exited cleanly")
}
