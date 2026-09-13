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

	_ "github.com/mattn/go-sqlite3"

	exiftoolbin "github.com/vigovlugt/imchlite/exiftool"
	"github.com/vigovlugt/imchlite/ffmpeg"
)

func main() {
	libraryLocation := flag.String("library-location", "", "path to the imchlite library")
	addr := flag.String("addr", "127.0.0.1:3000", "address the api server listens on")
	flag.Parse()

	if *libraryLocation == "" {
		log.Fatal("--library-location is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	f, err := ffmpeg.Extract()
	if err != nil {
		log.Fatalf("extract ffmpeg: %v", err)
	}
	defer f.Close()
	log.Printf("debug: extracted ffmpeg in %s", time.Since(start))

	start = time.Now()
	et, err := exiftoolbin.Extract()
	if err != nil {
		log.Fatalf("extract exiftool: %v", err)
	}
	defer et.Close()
	log.Printf("debug: extracted exiftool in %s", time.Since(start))

	db, err := openDatabase(*libraryLocation)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	err = initMigrations(ctx, db)
	if err != nil {
		log.Fatalf("init migrations: %v", err)
	}

	fileRepo := NewFileRepository(db)
	assetRepo := NewAssetRepository(db)
	processor := newProcessor(ctx, *libraryLocation, f, et, fileRepo, assetRepo)
	queue := newAssetQueue()
	state := newIndexerState()

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			processor.worker(queue, state)
		})
	}

	// var closeQueue sync.Once
	// closeQueueFn := func() { closeQueue.Do(func() { queue.Close() }) }

	go func() {
		log.Printf("indexing library %s", *libraryLocation)
		if err := indexLibrary(ctx, *libraryLocation, fileRepo, assetRepo, queue, state); err != nil {
			log.Printf("indexing failed: %v", err)
		} else {
			log.Printf("indexing completed")
		}
		// closeQueueFn()
	}()

	srv := newServer(*addr, state, assetRepo, *libraryLocation)
	if err := runServer(ctx, srv); err != nil {
		log.Fatalf("serve: %v", err)
	}

	// Server stopped (signal received): cancel indexing and drain the queue.
	// closeQueueFn()
	// wg.Wait()
}
