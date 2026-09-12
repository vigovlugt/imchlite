package main

import (
	"context"
	"flag"
	"log"

	_ "github.com/mattn/go-sqlite3"

	"github.com/vigovlugt/imchlite/ffmpeg"
)

func main() {
	libraryLocation := flag.String("library-location", "", "path to the imchlite library")
	flag.Parse()

	if *libraryLocation == "" {
		log.Fatal("--library-location is required")
	}

	ctx := context.Background()

	f, err := ffmpeg.Extract()
	if err != nil {
		log.Fatalf("extract ffmpeg: %v", err)
	}
	defer f.Close()

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
	queue := newAssetQueue()

	for range 2 {
		go processWorker(queue)
	}

	if err := indexLibrary(ctx, *libraryLocation, fileRepo, assetRepo, queue); err != nil {
		log.Fatalf("index library: %v", err)
	}
}
