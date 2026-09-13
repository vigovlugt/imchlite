package main

import (
	"context"
	"flag"
	"log"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	exiftoolbin "github.com/vigovlugt/imchlite/exiftool"
	"github.com/vigovlugt/imchlite/ffmpeg"
)

func main() {
	libraryLocation := flag.String("library-location", "", "path to the imchlite library")
	flag.Parse()

	if *libraryLocation == "" {
		log.Fatal("--library-location is required")
	}

	ctx := context.Background()

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

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			processor.worker(queue)
		})
	}

	if err := indexLibrary(ctx, *libraryLocation, fileRepo, assetRepo, queue); err != nil {
		log.Fatalf("index library: %v", err)
	}

	// Close the queue and wait for the workers to drain it.
	// queue.Close()
	wg.Wait()
}
