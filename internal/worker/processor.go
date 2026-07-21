/*
 * Copyright (C) 2026 Yukthi Systems Private Limited
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3
 * as published by the Free Software Foundation.
 *
 * This program is distributed in the hope  that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * version 3 along with this program. If not, see
 * <https://www.gnu.org/licenses/>.
 */

package worker

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"archive-email-downloader/internal/db"
	"archive-email-downloader/internal/logger"
	"archive-email-downloader/internal/zipsvc"
)

type ZipAdder interface {
	AddFile(workerID int, orgID string, name string, content io.Reader, size int64) (string, error)
}

type Processor struct {
	repo         *db.Repository
	zipPool      *zipsvc.Pool
	client       *http.Client
	apiKey       string
	log          *logger.Logger
	successCount *atomic.Int64
	totalCount   int64
	completionCh chan string
	jobID        string
}

func NewProcessor(
	repo *db.Repository,
	zipPool *zipsvc.Pool,
	apiKey string,
	log *logger.Logger,
	successCounter *atomic.Int64,
	total int64,
	completionCh chan string,
	numWorkers int,
	jobID string,
) *Processor {
	transport := &http.Transport{
		MaxIdleConns:        numWorkers * 2,
		MaxIdleConnsPerHost: numWorkers * 2,
		IdleConnTimeout:     90 * time.Second,
	}

	return &Processor{
		repo:         repo,
		zipPool:      zipPool,
		apiKey:       apiKey,
		client:       &http.Client{Timeout: 30 * time.Second, Transport: transport},
		log:          log,
		successCount: successCounter,
		totalCount:   total,
		completionCh: completionCh,
		jobID:        jobID,
	}
}

func (p *Processor) ProcessRecord(rec db.EmailRecord, workerID int) error {
	loc, err := db.ParseLocation(rec.ArchiveLocation)
	if err != nil {
		p.log.ErrorLog.Error("Parse location failed", "id", rec.ArchiveID, "error", err)
		return p.markFailed(rec.ArchiveID, fmt.Sprintf("Parse error: %v", err))
	}

	url := loc.BuildURL()

	var resp *http.Response
	const maxRetries = 3

	for attempt := 1; attempt <= maxRetries; attempt++ {

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return p.markFailed(rec.ArchiveID, fmt.Sprintf("Request build error: %v", err))
		}
		req.Header.Set("X-API-KEY", p.apiKey)

		resp, err = p.client.Do(req)

		if err == nil && resp.StatusCode == http.StatusOK {
			break // success
		}

		if attempt == maxRetries {
			var errStr string
			if resp != nil {
				errStr = fmt.Sprintf("API returned status %d after %d attempts", resp.StatusCode, maxRetries)
				p.log.ErrorLog.Error("API error", "id", rec.ArchiveID, "status", resp.StatusCode)
			} else {
				errStr = fmt.Sprintf("Network error after %d attempts: %v", maxRetries, err)
				p.log.ErrorLog.Error("Network error", "id", rec.ArchiveID, "error", err)
			}
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			return p.markFailed(rec.ArchiveID, errStr)
		}

		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}

		// Exponential backoff: 1s, 4s, 9s
		time.Sleep(time.Duration(attempt*attempt) * time.Second)
	}
	defer resp.Body.Close()

	fileName := fmt.Sprintf("%s.eml", rec.ArchiveID)

	zipName, err := p.zipPool.AddFile(workerID, p.jobID, fileName, resp.Body, rec.RawEmlSize)
	if err != nil {
		p.log.ErrorLog.Error("ZIP error", "id", rec.ArchiveID, "error", err)
		return p.markFailed(rec.ArchiveID, fmt.Sprintf("ZIP error: %v", err))
	}

	current := p.successCount.Add(1)
	p.log.ProcessLog.Info("Archived",
		"progress", fmt.Sprintf("%d/%d", current, p.totalCount),
		"id", rec.ArchiveID,
		"job", p.jobID,
		"zip", zipName,
	)

	p.completionCh <- rec.ArchiveID
	return nil
}

func (p *Processor) RunCompletionFlusher(batchSize int, flushInterval time.Duration) {
	batch := make([]string, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := p.repo.MarkCompletedBatch(batch); err != nil {
			p.log.ErrorLog.Error("Batch completion flush failed", "error", err, "count", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case id, ok := <-p.completionCh:
			if !ok {
				flush() // drain remaining on shutdown
				return
			}
			batch = append(batch, id)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (p *Processor) markFailed(id string, reason string) error {
	return p.repo.UpdateStatus(id, db.StatusFailed, reason)
}