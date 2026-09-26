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

	"github.com/vigovlugt/imchlite/internal/ai"
	"github.com/vigovlugt/imchlite/internal/api"
	"github.com/vigovlugt/imchlite/internal/database"
	exiftoolbin "github.com/vigovlugt/imchlite/internal/exiftool"
	"github.com/vigovlugt/imchlite/internal/ffmpeg"
	"github.com/vigovlugt/imchlite/internal/library"
	onnxruntime "github.com/vigovlugt/imchlite/internal/onnxruntime"
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

	start = time.Now()
	if err := onnxruntime.Setup(); err != nil {
		log.Fatalf("setup onnxruntime: %v", err)
	}
	clipDir, err := ai.Setup()
	if err != nil {
		log.Fatalf("extract clip visual model: %v", err)
	}
	clip, err := ai.NewClipVisual(clipDir)
	if err != nil {
		log.Fatalf("load clip visual model: %v", err)
	}
	defer clip.Close()
	log.Printf("debug: loaded clip visual model in %s", time.Since(start))

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
	queue := library.NewQueue()
	processor := library.NewProcessor(ctx, libraryLocation, f, fileRepo, assetRepo, clip, queue)
	state := library.NewIndexerState()

	// Clip tasks live only in memory; the clip_embedded_at column is the
	// durable marker. Re-derive any tasks lost by a previous restart.
	if n, err := library.EnqueuePendingClipTasks(ctx, assetRepo, queue); err != nil {
		log.Fatalf("recover pending clip tasks: %v", err)
	} else if n > 0 {
		log.Printf("re-enqueued %d pending clip tasks", n)
	}

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
	})

	srv := api.NewServer(*addr, state, assetRepo, libraryLocation, frontendHandler())
	if err := api.RunServer(ctx, srv); err != nil {
		log.Printf("serve: %v", err)
		return
	}

	// Server stopped (signal received): close the queue, canceling the
	// indexer's walk; workers drain the queue and exit. Pushes racing the
	// close are no-ops — any task dropped that way stays pending in the
	// database and is re-enqueued on the next startup. The extracted
	// ffmpeg/exiftool/clip cache stays on disk for the next run.
	queue.Close()
	indexWG.Wait()
	wg.Wait()
}
