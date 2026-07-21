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

package db

import (
	"time"
)

const (
	StatusPending    = "PENDING"
	StatusProcessing = "PROCESSING"
	StatusCompleted  = "COMPLETED"
	StatusFailed     = "FAILED"
)

type EmailRecord struct {
	ArchiveID       string    `db:"archive_id"`
	ArchiveTime     time.Time `db:"archive_time"`
	ArchiveLocation string    `db:"archive_location"`
	RawEmlSize      int64     `db:"raw_eml_size"`
	ExportStatus    string    `db:"export_status"`
	UpdatedAt       time.Time `db:"updated_at"`
	ErrorLog        string    `db:"error_log"`
	Retries         int       `db:"retries"`
}

type ParsedLocation struct {
	ServerIP string
	OrgID    string
	Domain   string
	Date     string
	ID       string
}
