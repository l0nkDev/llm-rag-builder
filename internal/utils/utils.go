package utils

import (
	"log"
	"os"
	"time"
)

func LogTiming(action, item string, duration time.Duration) {
	f, err := os.OpenFile("timing.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[Error] Failed to open timing.log: %v", err)
		return
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)
	logger.Printf("%s | ITEM: %s | DURATION: %v\n", action, item, duration)
}
