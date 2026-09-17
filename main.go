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
	workers := flag.Int("workers", 4, "number of parallel asset processors")
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
	exiftoolDir, err := exiftoolbin.Setup()
	if err != nil {
		log.Fatalf("extract exiftool: %v", err)
	}
	defer exiftoolbin.Teardown(exiftoolDir)
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
	processor := newProcessor(ctx, *libraryLocation, f, fileRepo, assetRepo)
	queue := newAssetQueue()
	state := newIndexerState()

	var wg sync.WaitGroup
	for range *workers {
		et, err := exiftoolbin.New(exiftoolDir)
		if err != nil {
			log.Fatalf("start exiftool: %v", err)
		}
		defer et.Close()

		wg.Go(func() {
			processor.worker(et, queue, state)
		})
	}

	var closeQueue sync.Once
	closeQueueFn := func() { closeQueue.Do(func() { queue.Close() }) }

	var indexWG sync.WaitGroup
	indexWG.Go(func() {
		log.Printf("indexing library %s", *libraryLocation)
		if err := indexLibrary(ctx, *libraryLocation, fileRepo, assetRepo, queue, state); err != nil {
			log.Printf("indexing failed: %v", err)
		} else {
			log.Printf("indexing completed")
		}
		closeQueueFn()
	})

	srv := newServer(*addr, state, assetRepo, *libraryLocation)
	if err := runServer(ctx, srv); err != nil {
		log.Printf("serve: %v", err)
		return
	}

	// Server stopped (signal received): the indexer stops walking on the
	// canceled context and closes the queue; workers drain it and exit.
	// Only then are the exiftool processes closed by the deferred Close.
	indexWG.Wait()
	wg.Wait()
}
