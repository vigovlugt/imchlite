package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/vigovlugt/imchlite/internal/api"
	"github.com/vigovlugt/imchlite/internal/database"
	exiftoolbin "github.com/vigovlugt/imchlite/internal/exiftool"
	"github.com/vigovlugt/imchlite/internal/ffmpeg"
	"github.com/vigovlugt/imchlite/internal/library"
	"github.com/vigovlugt/imchlite/internal/repository"
)

func main() {
	libraryLocationFlag := flag.String("library-location", "", "path to the imchlite library")
	addr := flag.String("addr", "127.0.0.1:3000", "address the api server listens on")
	workers := flag.Int("workers", 1, "number of parallel asset processors")
	flag.Parse()

	if *libraryLocationFlag == "" {
		if cwd, err := os.Getwd(); err == nil && filepath.Base(cwd) == ".imchlite" {
			*libraryLocationFlag = ".."
		} else {
			*libraryLocationFlag = "."
		}
	}
	libraryLocation, err := filepath.Abs(*libraryLocationFlag)
	if err != nil {
		log.Fatalf("get absolute path: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	ffmpegDir, err := ffmpeg.Setup()
	if err != nil {
		log.Fatalf("extract ffmpeg: %v", err)
	}
	log.Printf("debug: extracted ffmpeg in %s", time.Since(start))

	start = time.Now()
	exiftoolDir, err := exiftoolbin.Setup()
	if err != nil {
		log.Fatalf("extract exiftool: %v", err)
	}
	log.Printf("debug: extracted exiftool in %s", time.Since(start))

	f, err := ffmpeg.New(ffmpegDir)
	if err != nil {
		log.Fatalf("start ffmpeg: %v", err)
	}

	db, err := database.Open(libraryLocation)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	err = database.Migrate(ctx, db)
	if err != nil {
		log.Fatalf("init migrations: %v", err)
	}

	fileRepo := repository.NewFileRepository(db)
	assetRepo := repository.NewAsset(db)
	processor := library.NewProcessor(ctx, libraryLocation, f, fileRepo, assetRepo)
	queue := library.NewAssetQueue()
	state := library.NewIndexerState()

	var wg sync.WaitGroup
	for range *workers {
		et, err := exiftoolbin.New(exiftoolDir)
		if err != nil {
			log.Fatalf("start exiftool: %v", err)
		}
		defer et.Close()

		wg.Go(func() {
			processor.Worker(et, queue, state)
		})
	}

	var indexWG sync.WaitGroup
	indexWG.Go(func() {
		log.Printf("indexing library %s", libraryLocation)
		if err := library.IndexLibrary(ctx, libraryLocation, fileRepo, queue, state); err != nil {
			log.Printf("indexing failed: %v", err)
		} else {
			log.Printf("indexing completed")
		}
		queue.Close()
	})

	srv := api.NewServer(*addr, state, assetRepo, libraryLocation, frontendHandler())
	if err := api.RunServer(ctx, srv); err != nil {
		log.Printf("serve: %v", err)
		return
	}

	// Server stopped (signal received): the indexer stops walking on the
	// canceled context and closes the queue; workers drain it and exit.
	// Only then are the exiftool processes closed by the deferred Close.
	// The extracted ffmpeg/exiftool cache stays on disk for the next run.
	indexWG.Wait()
	wg.Wait()
}
