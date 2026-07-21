# Archive Email Downloader

A high-performance concurrent tool for downloading emails from an archive API and packaging them into ZIP files. Built in Go, designed to process millions of records efficiently.

---

## Project Structure

```
archive-email-downloader/
├── cmd/
│   ├── downloader/
│   │   └── main.go           # Entry point, worker pool, orchestration
│   └── cleanup/
│       └── main.go           # Cleanup utility for old archives
├── internal/
│   ├── db/
│   │   ├── models.go         # EmailRecord struct, status constants
│   │   ├── parser.go         # archive_location parser, URL builder
│   │   └── sqlite.go         # Repository: fetch, mark completed, batch flush
│   ├── logger/
│   │   └── logger.go         # Three-channel structured logger (errors/process/general)
│   ├── postScript/
│   │   └── postscript.go     # Post-download: email notifications, DB backup
│   ├── worker/
│   │   └── processor.go      # HTTP download, retry logic, ZIP write, progress tracking
│   └── zipsvc/
│       └── pool.go           # ZIP pool with fixed slots, buffered writes, auto-rotation
```

---

## How It Works

```
SQLite DB (PENDING records)
        │
        ▼
  [Prefetch Goroutine]  ──── fetches batches continuously
        │
        ▼
  [Jobs Channel]  (buffered: batch_size × 2)
        │
   ┌────┴────┐
   ▼         ▼
[Worker 1] [Worker 2] ... [Worker N]
   │         │
   │  HTTP download with retry + backoff
   │         │
   ▼         ▼
[ZIP Pool — N slots, each with its own mutex + 4MB buffer]
   │
   ▼
emails_0001.zip, emails_0002.zip ... (up to max size, then rotates)
   │
   ▼
[Completion Channel]
   │
   ▼
[Flusher Goroutine] — batch UPDATE COMPLETED in SQLite every 10s or 200 IDs
```

---

## Features

- **Concurrent downloads** — configurable worker pool, each worker runs independently
- **ZIP slot pool** — fixed number of concurrent ZIP files, auto-calculated from worker count
- **Buffered disk writes** — 4MB `bufio.Writer` per slot, far fewer syscalls vs writing directly
- **Batch DB completions** — IDs flushed in bulk, not one UPDATE per record
- **Crash recovery** — stale `PROCESSING` records reset to `PENDING` on every startup
- **Graceful shutdown** — `SIGINT` / `SIGTERM` drains workers and flushes DB before exit
- **Exponential backoff** — failed HTTP requests retry up to 3 times (1s, 4s, 9s)
- **Progress logging** — every archived email logs `progress=current/total`
- **Restart safe** — resumes ZIP numbering from highest existing file
- **Auto cleanup** — optional cron utility to delete archives older than 7 days

---

## Build

### Downloader

**Local (Windows):**
```powershell
go build -o archive-downloader.exe ./cmd/downloader
```

**Linux (Cross-Compilation from Windows):**
```powershell
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o archive-downloader ./cmd/downloader
```

**Linux (Native):**
```bash
GOOS=linux GOARCH=amd64 go build -o archive-downloader ./cmd/downloader
```

### Cleanup Utility

**Local:**
```bash
cd cmd/cleanup
go build -o cleanup-storage
```

**Linux (Cross-Compilation):**
```bash
GOOS=linux GOARCH=amd64 go build -o cleanup-storage ./cmd/cleanup
```

---

## Usage - Downloader

### Windows
```powershell
.\archive-downloader.exe `
  --job-id      "job-123" `
  --sqlite-path "sql.db" `
  --output-dir  "./storage" `
  --log-dir     "./logs" `
  --workers     50
```

### Linux
```bash
./archive-downloader \
  --job-id job-123 \
  --sqlite-path sql.db \
  --output-dir ./storage \
  --workers 50
```

---

## Usage - Cleanup Utility

Delete ZIP and DB files older than 7 days:

```bash
# Dry run (preview what will be deleted)
./cleanup-storage --dry-run --storage-dir=./storage

# Actually delete old files
./cleanup-storage --storage-dir=./storage

# Custom retention (14 days)
./cleanup-storage --storage-dir=./storage --max-age-days=14
```

### Cleanup Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--storage-dir` | `./storage` | Storage directory to clean |
| `--max-age-days` | `7` | Delete files older than N days |
| `--dry-run` | `false` | Preview deletions without actually deleting |

### Cleanup Output Structure

The utility processes this structure:
```
storage/
├── job123/
│   ├── emails_0001.zip    ← Deleted if > 7 days old
│   ├── emails_0002.zip    ← Deleted if > 7 days old
│   └── sql-lite.db        ← Deleted if > 7 days old
└── job456/
    └── ...
```

Empty job directories are automatically removed after cleanup.

### Cron Setup

**Daily cleanup at 2 AM:**
```bash
crontab -e
# Add this line:
0 2 * * * /path/to/cleanup-storage --storage-dir=/path/to/storage >> /var/log/cleanup.log 2>&1
```

**Other schedules:**
```bash
0 */6 * * *     # Every 6 hours
0 3 * * 0       # Weekly on Sunday at 3 AM
0 1 1 * *       # Monthly on 1st at 1 AM
```

---

## Environment Variables

Sensitive values can be supplied via environment variables instead of flags:

| Variable | Used by flag |
|---|---|
| `ARCHIVE_API_KEY` | `--api-key` |
| `SMTP_PASSWORD` | `--smtp-pass` |

Flags always take priority over environment variables.

---

## Downloader Flags

### Core

| Flag | Default | Description |
|------|---------|-------------|
| `--job-id` | *(required)* | Unique ID for the job. Used as the subfolder name in the output directory |
| `--api-key` | *(required)* | API key sent as `X-API-KEY` header. Can also be set via `ARCHIVE_API_KEY` env var |
| `--sqlite-path` | `sql.db` | Path to the SQLite database |
| `--output-dir` | `./storage` | Directory where ZIP files are saved |
| `--log-dir` | `./logs` | Directory where log files are saved |
| `--workers` | `50` | Number of concurrent download workers |
| `--zip-slots` | `0` (auto) | Concurrent ZIP files. `0` = auto-calculated as `workers/5`, min 5 |
| `--max-zip-size-mb` | `1024` | Max size per ZIP file in MB. Rotates to a new file when reached |
| `--batch-size` | `500` | Records fetched per SQLite query |
| `--flush-batch` | `200` | IDs batched per DB completion write |

### Email Notification (all required when `--mail-to` is set)

| Flag | Default | Description |
|------|---------|-------------|
| `--mail-to` | `""` | Email address to notify upon job completion |
| `--smtp-host` | `""` | SMTP server hostname |
| `--smtp-port` | `587` | SMTP server port |
| `--smtp-user` | `""` | SMTP username |
| `--smtp-pass` | `""` | SMTP password. Can also be set via `SMTP_PASSWORD` env var |
| `--smtp-from` | `""` | Sender email address |
| `--smtp-from-name` | `Archive Export Service` | Sender display name |
| `--archive-base-url` | `""` | Base URL for the archive portal included in the notification email |

---

## ZIP Behaviour

Each worker is assigned to a ZIP slot via `(workerID - 1) % zipSlots`. Multiple workers share a slot but contention is minimal since workers spend ~95% of their time on HTTP, not ZIP writes.

| Workers | Auto ZIP Slots | Workers per Slot |
|---------|---------------|-----------------|
| 50      | 10            | 5               |
| 100     | 20            | 5               |
| 200     | 40            | 5               |

- Each slot writes to its own file independently with a **4MB buffer**
- When a slot's ZIP hits `--max-zip-size-mb` it rotates to a new file automatically
- ZIP files will vary in size — this is expected and fine
- Final ZIP count depends on total data size and worker count

---

## Logs

Three separate log channels, each writes to its own file and to stdout:

```
logs/
├── general/app.log    # startup, shutdown, DB events
├── process/app.log    # per-email archived progress
└── errors/app.log     # failed downloads, ZIP errors, DB errors
```

---

## Recommended Settings

**For 1 million+ emails:**
```bash
--workers 50 --max-zip-size-mb 2048
```
At ~250 emails/sec this processes 1M emails in roughly 1–1.5 hours depending on network and server speed.

**If getting rate-limited (429s):** reduce `--workers` first before anything else.

**If getting DB lock errors:** increase `--flush-batch` so the flusher writes less frequently.

---

## Database Schema Expected

```sql
CREATE TABLE email_archive (
    archive_id       TEXT PRIMARY KEY,
    archive_time     DATETIME,
    archive_location TEXT,      -- format: serverIP:orgID:domain:date:id
    raw_eml_size     INTEGER,
    export_status    TEXT,      -- NULL / PENDING / PROCESSING / COMPLETED / FAILED
    updated_at       DATETIME,
    error_log        TEXT,
    retries          INTEGER
);
```

---

## Crash Recovery

On every startup the app runs:
```sql
UPDATE email_archive SET export_status = 'PENDING'
WHERE export_status = 'PROCESSING'
```
Any records that were mid-flight during a crash are automatically retried. No manual intervention needed.

---

## Resuming Jobs

The application supports smart resumption. If you stop the downloader and restart it with the same `--job-id`:
1.  **DB State**: It continues from where it left off (only processing `PENDING` records).
2.  **ZIP Indexing**: It automatically scans the job directory and starts the next ZIP file with a non-conflicting index (e.g., if `emails_0005.zip` exists, it starts with `emails_0006.zip`).

---

## Deployment (SCP)

To deploy the binaries and database to a remote Linux server:

**1. Copy the downloader:**
```bash
scp archive-downloader root@<server-ip>:/srv/archive-downloader/
```

**2. Copy the cleanup utility:**
```bash
scp cleanup-storage root@<server-ip>:/srv/archive-downloader/
```

**3. Copy the database:**
```bash
scp sql.db root@<server-ip>:/srv/archive-downloader/
```

**4. Run on server:**
```bash
ssh root@<server-ip>
cd /srv/archive-downloader/

# Make executable
chmod +x archive-downloader cleanup-storage

# Run downloader
./archive-downloader --job-id "my-job" --sqlite-path "sql.db"

# Setup cleanup cron
crontab -e
# Add: 0 2 * * * /srv/archive-downloader/cleanup-storage --storage-dir=/srv/archive-downloader/storage >> /var/log/cleanup.log 2>&1
```

---

## Complete Workflow Example

```bash
# 1. Build both utilities
go build -o archive-downloader ./cmd/downloader
go build -o cleanup-storage ./cmd/cleanup

# 2. Run downloader
export ARCHIVE_API_KEY="your-api-key-here"
export SMTP_PASSWORD="your-smtp-password"

./archive-downloader \
  --job-id=export-2026-03 \
  --sqlite-path=emails.db \
  --output-dir=/var/archive/storage \
  --workers=50 \
  --mail-to=admin@example.com \
  --smtp-host=smtp.example.com \
  --smtp-user=notifications \
  --smtp-from=no-reply@example.com \
  --archive-base-url=https://archive.example.com/export/eml
```

# 3. Setup automated cleanup
crontab -e
# Add: 0 2 * * * /path/to/cleanup-storage --storage-dir=/var/archive/storage --max-age-days=7

# 4. Monitor
tail -f logs/process/app.log
tail -f /var/log/cleanup.log
```