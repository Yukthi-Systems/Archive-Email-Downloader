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

package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // CGO-free driver
)

type Repository struct {
	conn *sql.DB
}

func NewRepository(dbPath string) (*Repository, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	_, err = db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 30000;
		PRAGMA cache_size = -32000;
	`)
	if err != nil {
		return nil, err
	}

	return &Repository{conn: db}, nil
}

func (r *Repository) ResetStalePROCESSING() error {
	_, err := r.conn.Exec(`
		UPDATE email_archive 
		SET export_status = 'PENDING', updated_at = CURRENT_TIMESTAMP 
		WHERE export_status = 'PROCESSING'
	`)
	return err
}

func (r *Repository) FetchBatch(limit int) ([]EmailRecord, error) {
	tx, err := r.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	query := `SELECT archive_id, archive_time, archive_location, raw_eml_size 
	          FROM email_archive 
	          WHERE export_status IS NULL 
	             OR TRIM(export_status) = '' 
	             OR export_status = '""' 
	             OR export_status = 'PENDING' 
	          LIMIT ?`
	rows, err := tx.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []EmailRecord
	var ids []string

	for rows.Next() {
		var rec EmailRecord
		if err := rows.Scan(&rec.ArchiveID, &rec.ArchiveTime, &rec.ArchiveLocation, &rec.RawEmlSize); err != nil {
			return nil, err
		}
		records = append(records, rec)
		ids = append(ids, rec.ArchiveID)
	}

	if len(records) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	updateQuery := fmt.Sprintf(
		"UPDATE email_archive SET export_status = 'PROCESSING', updated_at = CURRENT_TIMESTAMP WHERE archive_id IN (%s)",
		strings.Join(placeholders, ","),
	)

	_, err = tx.Exec(updateQuery, args...)
	if err != nil {
		return nil, err
	}

	return records, tx.Commit()
}

func (r *Repository) MarkCompletedBatch(ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	tx, err := r.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		`UPDATE email_archive 
		 SET export_status = 'COMPLETED', error_log = NULL, updated_at = CURRENT_TIMESTAMP 
		 WHERE archive_id IN (%s)`,
		strings.Join(placeholders, ","),
	)

	_, err = tx.Exec(query, args...)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) MarkCompleted(id string) error {
	query := `UPDATE email_archive SET 
	          export_status = 'COMPLETED', 
	          error_log = NULL,
	          updated_at = CURRENT_TIMESTAMP 
	          WHERE archive_id = ?`
	_, err := r.conn.Exec(query, id)
	return err
}

func (r *Repository) UpdateStatus(id string, status string, errLog string) error {
	query := `UPDATE email_archive SET 
	          export_status = ?, 
	          error_log = ?,
	          updated_at = CURRENT_TIMESTAMP 
	          WHERE archive_id = ?`
	_, err := r.conn.Exec(query, status, errLog, id)
	return err
}

func (r *Repository) Close() error {
	return r.conn.Close()
}

func (r *Repository) GetProgressStats() (int64, int64, int64, error) {
	var completed, failed, total int64

	err := r.conn.QueryRow("SELECT COUNT(*) FROM email_archive WHERE export_status = 'COMPLETED'").Scan(&completed)
	if err != nil {
		return 0, 0, 0, err
	}

	err = r.conn.QueryRow("SELECT COUNT(*) FROM email_archive WHERE export_status = 'FAILED'").Scan(&failed)
	if err != nil {
		return 0, 0, 0, err
	}

	err = r.conn.QueryRow("SELECT COUNT(*) FROM email_archive").Scan(&total)
	if err != nil {
		return 0, 0, 0, err
	}

	return completed, failed, total, nil
}

// Add this method to the Repository struct
func (r *Repository) ForceCheckpoint() error {
	// Force WAL checkpoint - writes all WAL data to main database file
	_, err := r.conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	if err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	
	// Force sync to disk
	_, err = r.conn.Exec("PRAGMA synchronous = FULL")
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	
	return nil
}