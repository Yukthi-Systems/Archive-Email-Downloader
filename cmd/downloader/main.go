/*
 * Copyright (C) 2026 Yukthi Systems Private Limited
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3
 * as published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * version 3 along with this program. If not, see
 * <https://www.gnu.org/licenses/>.
 */

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"archive-email-downloader/internal/db"
	"archive-email-downloader/internal/logger"
	postscript "archive-email-downloader/internal/postScript"
	"archive-email-downloader/internal/worker"
	"archive-email-downloader/internal/zipsvc"
)

func main() {
	// Core flags
	sqlitePath := flag.String("sqlite-path", "sql.db", "Path to SQLite file")
	outputDir := flag.String("output-dir", "./storage", "Where to save ZIPs")
	logDir := flag.String("log-dir", "./logs", "Where to save logs")
	maxZipMB := flag.Int("max-zip-size-mb", 1024, "Max ZIP size in MB (default 1024 = 1GB)")
	zipSlots := flag.Int("zip-slots", 0, "Number of concurrent ZIP files (0 = auto: workers/5, min 5)")
	numWorkers := flag.Int("workers", 50, "Number of concurrent download workers")
	batchSize := flag.Int("batch-size", 500, "Records to fetch per DB query")
	flushBatch := flag.Int("flush-batch", 200, "IDs to batch per DB completion write")
	jobID := flag.String("job-id", "", "Job ID for organizing files (required)")
	mailTo := flag.String("mail-to", "", "Email address to notify upon completion")
	version := flag.String("version", "1.1.1", "Application version")

	// Storage API key — no default; pass via --api-key or ARCHIVE_API_KEY env var
	apiKey := flag.String("api-key", "", "Storage API Key (or set ARCHIVE_API_KEY env var)")

	// SMTP flags — only required when --mail-to is set
	smtpHost := flag.String("smtp-host", "", "SMTP server hostname (required with --mail-to)")
	smtpPort := flag.String("smtp-port", "587", "SMTP server port")
	smtpUser := flag.String("smtp-user", "", "SMTP username (required with --mail-to)")
	smtpPass := flag.String("smtp-pass", "", "SMTP password (required with --mail-to, or set SMTP_PASSWORD env var)")
	smtpFrom := flag.String("smtp-from", "", "Sender email address (required with --mail-to)")
	smtpFromName := flag.String("smtp-from-name", "Archive Export Service", "Sender display name")

	// Archive portal URL — base URL used in notification emails
	archiveBaseURL := flag.String("archive-base-url", "", "Base URL for the archive portal (e.g. https://archive.example.com/export/eml)")

	flag.Parse()

	log.Printf("Version: %s\n", *version)

	if *jobID == "" {
		log.Fatal("--job-id flag is required")
	}

	// Resolve API key: flag takes priority, then env var
	resolvedAPIKey := *apiKey
	if resolvedAPIKey == "" {
		resolvedAPIKey = os.Getenv("ARCHIVE_API_KEY")
	}
	if resolvedAPIKey == "" {
		log.Fatal("API key is required: use --api-key flag or set the ARCHIVE_API_KEY environment variable")
	}

	// Resolve SMTP password: flag takes priority, then env var
	resolvedSMTPPass := *smtpPass
	if resolvedSMTPPass == "" {
		resolvedSMTPPass = os.Getenv("SMTP_PASSWORD")
	}

	// Validate SMTP fields if --mail-to is set
	if *mailTo != "" {
		if *smtpHost == "" || *smtpUser == "" || resolvedSMTPPass == "" || *smtpFrom == "" {
			log.Fatal("--smtp-host, --smtp-user, --smtp-pass (or SMTP_PASSWORD env var), and --smtp-from are required when --mail-to is set")
		}
		if *archiveBaseURL == "" {
			log.Fatal("--archive-base-url is required when --mail-to is set")
		}
	}

	smtpCfg := postscript.SMTPConfig{
		Host:     *smtpHost,
		Port:     *smtpPort,
		Username: *smtpUser,
		Password: resolvedSMTPPass,
		From:     *smtpFrom,
		FromName: *smtpFromName,
	}

	log.Printf("mail-to argument provided: %s\n", *mailTo)

	if *zipSlots <= 0 {
		*zipSlots = *numWorkers / 5
		if *zipSlots < 5 {
			*zipSlots = 5
		}
	}

	appLog, err := logger.New(*logDir)
	if err != nil {
		log.Fatalf("Failed to init loggers: %v", err)
	}
	appLog.GeneralLog.Info("Application starting up...",
		"job_id", *jobID,
		"workers", *numWorkers,
		"version", *version,
		"zip_slots", *zipSlots,
		"max_zip_mb", *maxZipMB,
	)

	repo, err := db.NewRepository(*sqlitePath)
	if err != nil {
		appLog.GeneralLog.Error("Failed to open DB", "error", err)
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer repo.Close()

	if err := repo.ResetStalePROCESSING(); err != nil {
		log.Fatalf("Failed to reset stale PROCESSING records: %v", err)
	}

	completed, failed, total, err := repo.GetProgressStats()
	if err != nil {
		log.Fatalf("Failed to calculate DB stats: %v", err)
	}
	appLog.GeneralLog.Info("Database scanned", "completed", completed, "failed", failed, "total", total)

	var successCounter atomic.Int64
	successCounter.Store(completed)

	zipPool, err := zipsvc.NewPool(*outputDir, *maxZipMB, *zipSlots)
	if err != nil {
		log.Fatalf("Failed to create ZIP pool: %v", err)
	}

	// Resume logic
	jobDir := filepath.Join(*outputDir, *jobID)
	if maxIdx := zipsvc.FindMaxIndex(jobDir); maxIdx > 0 {
		appLog.GeneralLog.Info("Existing ZIPs found, resuming index", "max_idx", maxIdx)
		zipPool.SetStartingIndex(maxIdx)
	}

	completionCh := make(chan string, *batchSize**numWorkers)

	processor := worker.NewProcessor(
		repo, zipPool, resolvedAPIKey, appLog,
		&successCounter, total,
		completionCh, *numWorkers,
		*jobID,
	)

	jobs := make(chan db.EmailRecord, *batchSize*2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("\nShutdown signal received. Draining workers...")
		appLog.GeneralLog.Info("Shutdown signal received")
		cancel()
	}()

	var flusherWg sync.WaitGroup
	flusherWg.Add(1)
	go func() {
		defer flusherWg.Done()
		processor.RunCompletionFlusher(*flushBatch, 10*time.Second)
	}()

	var workerWg sync.WaitGroup
	for w := 1; w <= *numWorkers; w++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			for job := range jobs {
				if err := processor.ProcessRecord(job, workerID); err != nil {
					appLog.ErrorLog.Error("Worker failed job",
						"worker_id", workerID,
						"archive_id", job.ArchiveID,
						"error", err,
					)
				}
			}
		}(w)
	}

	log.Println("Starting download process...")
	appLog.GeneralLog.Info("Download process started")

	go func() {
		defer close(jobs)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				records, err := repo.FetchBatch(*batchSize)
				if err != nil {
					appLog.GeneralLog.Error("DB Fetch Error", "error", err)
					log.Printf("DB Fetch Error: %v", err)
					return
				}
				if len(records) == 0 {
					log.Println("No more pending records. Waiting for workers to finish...")
					appLog.GeneralLog.Info("All records fetched")
					return
				}
				for _, rec := range records {
					select {
					case jobs <- rec:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	workerWg.Wait()
	log.Println("All workers finished. Flushing final batch...")
	appLog.GeneralLog.Info("All workers finished, flushing completion batch")

	// CRITICAL FIX: Close completion channel and wait for flusher BEFORE copying database
	// This ensures all COMPLETED statuses are written to the database before we copy it
	close(completionCh)
	flusherWg.Wait()

	log.Println("Flusher finished. Closing ZIP pool...")
	appLog.GeneralLog.Info("Flusher finished, closing ZIP pool")
	if err := zipPool.Close(); err != nil {
		appLog.ErrorLog.Error("ZIP pool close failed", "error", err)
		log.Printf("Warning: Failed to close ZIP pool: %v", err)
	}

	log.Println("Flusher finished. Syncing database to disk...")
	appLog.GeneralLog.Info("Flusher finished, forcing database checkpoint")

	// Force database checkpoint before copying
	// This writes all WAL (Write-Ahead Log) data into the main database file
	if err := repo.ForceCheckpoint(); err != nil {
		appLog.ErrorLog.Error("Database checkpoint failed", "error", err)
		log.Fatalf("Failed to checkpoint database: %v", err)
	}

	log.Println("Database synced. Running post-actions...")
	appLog.GeneralLog.Info("Database checkpoint complete, starting post-actions")

	// Now all DB updates are complete AND written to disk, safe to copy
	if err := postscript.RunPostActions(*outputDir, *sqlitePath, *jobID, *mailTo, smtpCfg, *archiveBaseURL, appLog); err != nil {
		appLog.ErrorLog.Error("Post-action failed", "error", err)
	}

	log.Println("System exited cleanly.")
	appLog.GeneralLog.Info("System exited cleanly")
}
