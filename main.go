package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/vigovlugt/imchlite/internal/ai"
	"github.com/vigovlugt/imchlite/internal/api"
	"github.com/vigovlugt/imchlite/internal/database"
	"github.com/vigovlugt/imchlite/internal/disk"
	exiftoolbin "github.com/vigovlugt/imchlite/internal/exiftool"
	"github.com/vigovlugt/imchlite/internal/ffmpeg"
	"github.com/vigovlugt/imchlite/internal/library"
	onnxruntime "github.com/vigovlugt/imchlite/internal/onnxruntime"
	"github.com/vigovlugt/imchlite/internal/repository"
	"github.com/vigovlugt/imchlite/internal/vec1"
)

func main() {
	libraryDirFlag := flag.String("library-dir", "", "path to the imchlite library")
	dataDirFlag := flag.String("data-dir", "", "path to the imchlite data directory (database, thumbnails); defaults to <library-dir>/.imchlite")
	addr := flag.String("addr", "127.0.0.1:3000", "address the api server listens on")
	workers := flag.Int("workers", runtime.NumCPU(), "number of parallel asset processors")
	noBrowser := flag.Bool("no-browser", false, "do not open the frontend in a browser on startup")
	retryFailed := flag.Bool("retry-failed", false, "retry asset processing steps (thumbnail, clip) that failed in a previous run")
	serveOnly := flag.Bool("serve-only", false, "only run the api server; do not index the library or process assets")
	flag.Parse()

	if *libraryDirFlag == "" {
		if cwd, err := os.Getwd(); err == nil && filepath.Base(cwd) == ".imchlite" {
			*libraryDirFlag = ".."
		} else {
			*libraryDirFlag = "."
		}
	}
	libraryDir, err := filepath.Abs(*libraryDirFlag)
	if err != nil {
		log.Fatalf("get absolute path: %v", err)
	}
	if *dataDirFlag == "" {
		*dataDirFlag = filepath.Join(libraryDir, ".imchlite")
	}
	dataDir, err := filepath.Abs(*dataDirFlag)
	if err != nil {
		log.Fatalf("get absolute path: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	if err := onnxruntime.Setup(); err != nil {
		log.Fatalf("setup onnxruntime: %v", err)
	}
	textualDir, err := ai.SetupTextual(ctx)
	if err != nil {
		log.Fatalf("download clip textual model: %v", err)
	}
	textual, err := ai.NewClipTextual(textualDir)
	if err != nil {
		log.Fatalf("create clip textual model: %v", err)
	}
	defer textual.Close()
	log.Printf("debug: downloaded clip textual model in %s", time.Since(start))

	start = time.Now()
	vec1Dir, err := vec1.Setup()
	if err != nil {
		log.Fatalf("extract vec1 extension: %v", err)
	}
	log.Printf("debug: extracted vec1 extension in %s", time.Since(start))

	db, err := database.Open(dataDir, vec1.LibraryPath(vec1Dir))
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	err = database.Migrate(ctx, db)
	if err != nil {
		log.Fatalf("init migrations: %v", err)
	}

	assetRepo := repository.NewAsset(db)
	state := library.NewIndexerState()
	queue := library.NewQueue()
	var wg sync.WaitGroup

	if *serveOnly {
		state.Complete()
	} else {
		start = time.Now()
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
		clipDir, err := ai.Setup(ctx)
		if err != nil {
			log.Fatalf("download clip visual model: %v", err)
		}
		clip, err := ai.NewClipVisual(clipDir)
		if err != nil {
			log.Fatalf("create clip visual model: %v", err)
		}
		defer clip.Close()
		log.Printf("debug: downloaded clip visual model in %s", time.Since(start))

		f, err := ffmpeg.New(ffmpegDir)
		if err != nil {
			log.Fatalf("start ffmpeg: %v", err)
		}

		fileRepo := repository.NewFileRepository(db)
		disk := disk.New()
		processor := library.NewProcessor(ctx, libraryDir, dataDir, f, disk, fileRepo, assetRepo, clip, *retryFailed)

		library.EnqueueIndexTask(queue)

		// Asset tasks live only in memory; the per-step status columns are the
		// durable marker. Re-derive any tasks lost by a previous restart.
		if n, err := library.EnqueuePendingAssetTasks(ctx, assetRepo, queue, *retryFailed); err != nil {
			log.Fatalf("recover pending asset tasks: %v", err)
		} else if n > 0 {
			log.Printf("re-enqueued %d pending asset tasks", n)
		}

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
	}

	srv := api.NewServer(*addr, state, assetRepo, libraryDir, dataDir, textual, frontendHandler())
	if err := api.RunServer(ctx, srv, !*noBrowser); err != nil {
		log.Printf("serve: %v", err)
		return
	}

	// Server stopped (signal received): the canceled context stops an
	// in-progress walk; close the queue so workers drain it and exit. Pushes racing the
	// close are no-ops — any task dropped that way stays pending in the
	// database and is re-enqueued on the next startup. The extracted
	// ffmpeg/exiftool/clip cache stays on disk for the next run.
	queue.Close()
	wg.Wait()
}
