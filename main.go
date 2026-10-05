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
	exiftoolbin "github.com/vigovlugt/imchlite/internal/clients/exiftool"
	"github.com/vigovlugt/imchlite/internal/clients/ffmpeg"
	onnxruntime "github.com/vigovlugt/imchlite/internal/clients/onnxruntime"
	"github.com/vigovlugt/imchlite/internal/clients/vec1"
	"github.com/vigovlugt/imchlite/internal/database"
	"github.com/vigovlugt/imchlite/internal/disk"
	"github.com/vigovlugt/imchlite/internal/repository"
	"github.com/vigovlugt/imchlite/internal/tasks"
	"github.com/vigovlugt/imchlite/internal/utils"
)

func main() {
	// Deferred first so it runs last, after the other deferred cleanups.
	exitCode := 0
	defer func() {
		if exitCode != 0 {
			os.Exit(exitCode)
		}
	}()

	libraryDirFlag := flag.String("library-dir", "", "path to the imchlite library")
	dataDirFlag := flag.String("data-dir", "", "path to the imchlite data directory (database, thumbnails); defaults to <library-dir>/.imchlite")
	addr := flag.String("addr", "127.0.0.1:3000", "address the api server listens on")
	workers := flag.Int("workers", runtime.NumCPU(), "number of parallel asset processors")
	noBrowser := flag.Bool("no-browser", false, "do not open the frontend in a browser on startup")
	retryFailed := flag.Bool("retry-failed", false, "retry asset processing steps (thumbnail, clip) that failed in a previous run")
	serveOnly := flag.Bool("serve-only", false, "only run the api server; do not index the library or process assets")
	indexOnly := flag.Bool("index-only", false, "index the library and process all assets, then exit; do not run the api server")
	var excludes utils.Excludes
	flag.Func("exclude", "glob of library paths or names to skip while indexing, e.g. \"*.mov\" or \"Directory\" (repeatable)", func(v string) error {
		excludes = append(excludes, v)
		return nil
	})
	flag.Parse()

	if *serveOnly && *indexOnly {
		log.Fatalf("--serve-only and --index-only are mutually exclusive")
	}

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

	// The runtime loads in the background; the clip models wait for it
	// before creating their sessions.
	rt := onnxruntime.Load()
	go func() {
		if err := rt.Wait(); err != nil {
			log.Fatalf("setup onnxruntime: %v", err)
		}
	}()

	// The textual model only serves search queries, so the api needs it but
	// indexing does not.
	var textual *ai.ClipTextual
	if !*indexOnly {
		start := time.Now()
		textualDir, err := ai.SetupTextual(ctx)
		if err != nil {
			log.Fatalf("download clip textual model: %v", err)
		}
		textual, err = ai.NewClipTextual(textualDir, rt)
		if err != nil {
			log.Fatalf("create clip textual model: %v", err)
		}
		defer textual.Close()
		log.Printf("debug: set up clip textual model in %s", time.Since(start))
	}

	start := time.Now()
	vec1Dir, err := vec1.Setup()
	if err != nil {
		log.Fatalf("extract vec1 extension: %v", err)
	}
	log.Printf("debug: set up vec1 extension in %s", time.Since(start))

	db, err := database.Open(dataDir, vec1.LibraryPath(vec1Dir))
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	err = database.Migrate(ctx, db)
	if err != nil {
		log.Fatalf("init migrations: %v", err)
	}

	// ffmpeg serves both the indexer's thumbnails and the api's previews.
	start = time.Now()
	ffmpegDir, err := ffmpeg.Setup()
	if err != nil {
		log.Fatalf("extract ffmpeg: %v", err)
	}
	log.Printf("debug: set up ffmpeg in %s", time.Since(start))
	f, err := ffmpeg.New(ffmpegDir)
	if err != nil {
		log.Fatalf("start ffmpeg: %v", err)
	}

	assetRepo := repository.NewAsset(db)
	state := tasks.NewIndexerState()
	queue := tasks.NewQueue()
	var wg sync.WaitGroup

	if *serveOnly {
		state.Complete()
	} else {
		start = time.Now()
		exiftoolDir, err := exiftoolbin.Setup()
		if err != nil {
			log.Fatalf("extract exiftool: %v", err)
		}
		log.Printf("debug: set up exiftool in %s", time.Since(start))

		start = time.Now()
		clipDir, err := ai.Setup(ctx)
		if err != nil {
			log.Fatalf("download clip visual model: %v", err)
		}
		clip, err := ai.NewClipVisual(clipDir, rt)
		if err != nil {
			log.Fatalf("create clip visual model: %v", err)
		}
		defer clip.Close()
		log.Printf("debug: set up clip visual model in %s", time.Since(start))

		fileRepo := repository.NewFileRepository(db)
		disk := disk.New()
		go disk.Watch(ctx, 30*time.Second)
		processor := tasks.NewProcessor(ctx, libraryDir, dataDir, excludes, f, disk, fileRepo, assetRepo, clip, *retryFailed)

		// Indexing also re-enqueues asset tasks lost by a previous restart.
		tasks.EnqueueIndexTask(queue)

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

	if *indexOnly {
		// Every task is pushed by the initial index task or by a task
		// in flight, so an idle queue means all work is done.
		select {
		case <-queue.Idle():
			log.Printf("all tasks finished, exiting")
		case <-ctx.Done():
		}
	} else {
		srv := api.NewServer(*addr, state, assetRepo, libraryDir, dataDir, textual, f, frontendHandler())
		if err := api.RunServer(ctx, srv, !*noBrowser); err != nil {
			log.Printf("serve: %v", err)
			return
		}
	}

	// Server stopped or all tasks finished (or signal received): the
	// canceled context stops an in-progress walk; close the queue so workers
	// drain it and exit. Pushes racing the
	// close are no-ops — any task dropped that way stays pending in the
	// database and is re-enqueued on the next startup. The extracted
	// ffmpeg/exiftool/clip cache stays on disk for the next run.
	queue.Close()
	wg.Wait()

	if *indexOnly && state.Status().Failed {
		exitCode = 1
	}
}
