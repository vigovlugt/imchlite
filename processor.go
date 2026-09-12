package main

import (
	"log"

	"golang.design/x/chann"
)

type assetTask struct {
	FileID int64
	Path   string
}

func newAssetQueue() *chann.Chann[assetTask] {
	return chann.New[assetTask]()
}

// processWorker consumes asset tasks from the queue until it is closed.
func processWorker(queue *chann.Chann[assetTask]) {
	for task := range queue.Out() {
		log.Printf("processed file=%d path=%s", task.FileID, task.Path)
	}
}
