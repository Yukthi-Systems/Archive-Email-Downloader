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

package zipsvc

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// zipSlot is one independently managed ZIP file.
// Each slot has its own mutex so only workers assigned to this slot
// ever contend — workers on other slots are completely unaffected.
type zipSlot struct {
	mu          sync.Mutex
	file        *os.File
	bufWriter   *bufio.Writer // batches small writes → far fewer syscalls
	writer      *zip.Writer
	currentSize int64
	fileIndex   int32  // index of the ZIP file this slot is currently writing to
	currentOrg  string // tracks which org this slot is currently writing for
}

// Pool manages a fixed number of ZIP slots shared across all workers.
//
// Workers are assigned to slots via:
//
//	slot = (workerID - 1) % numSlots
//
// This keeps the number of concurrent ZIP files bounded and predictable.
// Each slot rotates into a new file independently when it hits maxSize.
// The shared fileCounter guarantees unique filenames across all slots.
//
// Each slot can now handle multiple organizations by creating org-specific
// subdirectories within the base output directory.
type Pool struct {
	baseOutputDir string   // base directory, org folders created inside
	maxSize       int64    // bytes per ZIP file
	slots         []*zipSlot
	numSlots      int
	fileCounter   atomic.Int32 // global — ensures unique ZIP filenames
	orgDirs       sync.Map     // cache of created org directories to avoid repeated mkdir
}

// NewPool creates a Pool with a fixed number of ZIP slots.
//
//	outputDir — directory where ZIP files are written
//	maxSizeMB — max size per ZIP in megabytes (0 = unlimited)
//	numSlots  — number of concurrent ZIP files
//	           recommended: max(5, workers/20)
func NewPool(outputDir string, maxSizeMB int, numSlots int) (*Pool, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output dir: %w", err)
	}
	if numSlots < 1 {
		numSlots = 1
	}

	var maxBytes int64
	if maxSizeMB > 0 {
		maxBytes = int64(maxSizeMB) * 1024 * 1024
	} else {
		maxBytes = 1<<63 - 1 // effectively unlimited
	}

	slots := make([]*zipSlot, numSlots)
	for i := range slots {
		slots[i] = &zipSlot{}
	}

	return &Pool{
		baseOutputDir: outputDir,
		maxSize:       maxBytes,
		slots:         slots,
		numSlots:      numSlots,
	}, nil
}

// SetStartingIndex allows initializing the file counter to a specific value.
// Useful when resuming a job to avoid overwriting existing ZIP files.
func (p *Pool) SetStartingIndex(n int32) {
	p.fileCounter.Store(n)
}

// FindMaxIndex scans the given directory for files matching "emails_NNNN.zip"
// and returns the highest NNNN found. Returns 0 if none found.
func FindMaxIndex(dir string) int32 {
	var maxIdx int32
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		var idx int32
		_, err := fmt.Sscanf(f.Name(), "emails_%d.zip", &idx)
		if err == nil && idx > maxIdx {
			maxIdx = idx
		}
	}
	return maxIdx
}

// slotFor returns the ZIP slot assigned to a given workerID (1-based).
func (p *Pool) slotFor(workerID int) *zipSlot {
	return p.slots[(workerID-1)%p.numSlots]
}

// rotate opens a brand-new ZIP file for the given slot and organization.
// Caller MUST hold slot.mu before calling.
func (p *Pool) rotate(slot *zipSlot, orgID string) error {
	if slot.writer != nil {
		// Flush the zip writer first so it writes its end-of-central-directory
		if err := slot.writer.Close(); err != nil {
			return fmt.Errorf("close zip writer: %w", err)
		}
		// Flush the bufio buffer to disk before closing the file
		if err := slot.bufWriter.Flush(); err != nil {
			return fmt.Errorf("flush buf writer: %w", err)
		}
		if err := slot.file.Close(); err != nil {
			return fmt.Errorf("close zip file: %w", err)
		}
		slot.writer    = nil
		slot.bufWriter = nil
		slot.file      = nil
	}

	// Ensure org directory exists
	orgDir := filepath.Join(p.baseOutputDir, orgID)
	if _, exists := p.orgDirs.Load(orgID); !exists {
		if err := os.MkdirAll(orgDir, 0755); err != nil {
			return fmt.Errorf("create org dir %s: %w", orgDir, err)
		}
		p.orgDirs.Store(orgID, true)
	}

	idx := p.fileCounter.Add(1)
	path := filepath.Join(orgDir, fmt.Sprintf("emails_%04d.zip", idx))

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create zip file %s: %w", path, err)
	}

	// 4MB buffer — batches many small EML writes into large disk writes,
	// dramatically reducing syscall overhead vs writing directly to the file.
	bw := bufio.NewWriterSize(f, 4*1024*1024)

	slot.file        = f
	slot.bufWriter   = bw
	slot.writer      = zip.NewWriter(bw) // zip writes into the buffer, not the file directly
	slot.currentSize = 0
	slot.fileIndex   = idx
	slot.currentOrg  = orgID
	return nil
}

// AddFile writes an EML file into the ZIP slot assigned to workerID for the given organization.
//
// If the slot is currently writing to a different organization's ZIP, it will rotate
// to a new file for the new organization. This ensures each org's data stays separate.
//
// Contention only occurs between workers that share the same slot.
// With workers/20 slots, ~20 workers share a slot — but since each
// worker spends 95%+ of its time on HTTP (not ZIP writes), actual
// wait time is near zero in practice.
func (p *Pool) AddFile(workerID int, orgID string, name string, content io.Reader, size int64) (string, error) {
	slot := p.slotFor(workerID)

	slot.mu.Lock()
	defer slot.mu.Unlock()

	// Rotate if:
	// 1. First write ever to this slot, OR
	// 2. Organization changed (data separation), OR
	// 3. This file would push the ZIP over the size limit
	if slot.writer == nil || slot.currentOrg != orgID || slot.currentSize+size > p.maxSize {
		if err := p.rotate(slot, orgID); err != nil {
			return "", err
		}
	}

	// zip.Store = no compression.
	// EML files are plain text but compressing them during a high-throughput
	// download loop wastes CPU for very little gain.
	w, err := slot.writer.CreateHeader(&zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	})
	if err != nil {
		return "", fmt.Errorf("create zip entry: %w", err)
	}

	n, err := io.Copy(w, content)
	if err != nil {
		return "", fmt.Errorf("write zip entry: %w", err)
	}
	slot.currentSize += n

	// DO NOT flush after every entry — that defeats the entire purpose of
	// the buffer. The bufio.Writer accumulates writes and flushes in 4MB
	// chunks automatically. We only flush explicitly on rotate/close.

	return fmt.Sprintf("%s/emails_%04d.zip", orgID, slot.fileIndex), nil
}

// Close flushes and closes every open ZIP slot.
// Must be called after all workers have exited.
func (p *Pool) Close() error {
	var firstErr error
	for i, slot := range p.slots {
		slot.mu.Lock()
		if slot.writer != nil {
			if err := slot.writer.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("slot %d writer close: %w", i, err)
			}
			if err := slot.bufWriter.Flush(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("slot %d buf flush: %w", i, err)
			}
			if err := slot.file.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("slot %d file close: %w", i, err)
			}
			slot.writer    = nil
			slot.bufWriter = nil
			slot.file      = nil
		}
		slot.mu.Unlock()
	}
	return firstErr
}