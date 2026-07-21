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
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"
)

func main() {
	storageDir := flag.String("storage-dir", "./storage", "Storage directory to clean")
	maxAgeDays := flag.Int("max-age-days", 7, "Delete files older than this many days")
	dryRun := flag.Bool("dry-run", false, "Show what would be deleted without actually deleting")
	flag.Parse()

	log.Printf("Starting cleanup: dir=%s, max_age=%d days, dry_run=%v", *storageDir, *maxAgeDays, *dryRun)

	cutoffTime := time.Now().Add(-time.Duration(*maxAgeDays) * 24 * time.Hour)
	log.Printf("Deleting files older than: %s", cutoffTime.Format(time.RFC3339))

	var totalSize int64
	var fileCount int
	var errorCount int

	err := filepath.Walk(*storageDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error accessing %s: %v", path, err)
			errorCount++
			return nil
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Only process .zip and .db files
		ext := filepath.Ext(path)
		if ext != ".zip" && ext != ".db" {
			return nil
		}

		// Check if file is older 
		if info.ModTime().Before(cutoffTime) {
			size := info.Size()
			age := time.Since(info.ModTime())

			if *dryRun {
				log.Printf("[DRY-RUN] Would delete: %s (%.2f MB, %d days old)",
					path, float64(size)/1024/1024, int(age.Hours()/24))
			} else {
				log.Printf("Deleting: %s (%.2f MB, %d days old)",
					path, float64(size)/1024/1024, int(age.Hours()/24))
				
				if err := os.Remove(path); err != nil {
					log.Printf("Failed to delete %s: %v", path, err)
					errorCount++
					return nil
				}
			}

			totalSize += size
			fileCount++
		}

		return nil
	})

	if err != nil {
		log.Fatalf("Failed to walk directory: %v", err)
	}

	// Clean up empty job directories
	if !*dryRun {
		cleanEmptyDirs(*storageDir)
	}

	log.Printf("Cleanup complete: %d files, %.2f GB freed, %d errors",
		fileCount, float64(totalSize)/1024/1024/1024, errorCount)
}

// cleanEmptyDirs removes empty job directories
func cleanEmptyDirs(storageDir string) {
	entries, err := os.ReadDir(storageDir)
	if err != nil {
		log.Printf("Failed to read storage dir: %v", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirPath := filepath.Join(storageDir, entry.Name())
		isEmpty, err := isDirEmpty(dirPath)
		if err != nil {
			log.Printf("Error checking dir %s: %v", dirPath, err)
			continue
		}

		if isEmpty {
			log.Printf("Removing empty directory: %s", dirPath)
			if err := os.Remove(dirPath); err != nil {
				log.Printf("Failed to remove dir %s: %v", dirPath, err)
			}
		}
	}
}

func isDirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}