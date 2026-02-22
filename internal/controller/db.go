package controller

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"youdl/internal/model"
)

type DB struct {
	db *sql.DB
}

func NewDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite single-writer
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id            TEXT PRIMARY KEY,
			url           TEXT NOT NULL,
			title         TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'pending',
			mode          TEXT NOT NULL DEFAULT '',
			video_itag    TEXT NOT NULL DEFAULT '',
			audio_itag    TEXT NOT NULL DEFAULT '',
			file_path     TEXT NOT NULL DEFAULT '',
			file_name     TEXT NOT NULL DEFAULT '',
			error         TEXT NOT NULL DEFAULT '',
			worker_id     TEXT NOT NULL DEFAULT '',
			retries       INTEGER NOT NULL DEFAULT 0,
			submitter_ip   TEXT NOT NULL DEFAULT '',
			proxy_note     TEXT NOT NULL DEFAULT '',
			trim_start     TEXT NOT NULL DEFAULT '',
			trim_end       TEXT NOT NULL DEFAULT '',
			trim_last_secs INTEGER NOT NULL DEFAULT 0,
			created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at    DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS job_formats (
			job_id    TEXT NOT NULL,
			itag      TEXT NOT NULL DEFAULT '',
			quality   TEXT NOT NULL DEFAULT '',
			mime_type TEXT NOT NULL DEFAULT '',
			container TEXT NOT NULL DEFAULT '',
			type      TEXT NOT NULL DEFAULT '',
			bitrate   INTEGER NOT NULL DEFAULT 0,
			width     INTEGER NOT NULL DEFAULT 0,
			height    INTEGER NOT NULL DEFAULT 0,
			fps       INTEGER NOT NULL DEFAULT 0,
			label     TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (job_id, itag),
			FOREIGN KEY (job_id) REFERENCES jobs(id)
		);
		CREATE TABLE IF NOT EXISTS workers (
			id          TEXT PRIMARY KEY,
			addr        TEXT NOT NULL DEFAULT '',
			max_jobs    INTEGER NOT NULL DEFAULT 2,
			active_jobs INTEGER NOT NULL DEFAULT 0,
			cool_until  DATETIME NOT NULL DEFAULT (datetime('now')),
			last_seen   DATETIME NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		return err
	}
	// Idempotent column additions for existing installations.
	// SQLite errors if a column already exists; we ignore that error intentionally.
	db.Exec(`ALTER TABLE jobs ADD COLUMN submitter_ip TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE jobs ADD COLUMN proxy_note TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE jobs ADD COLUMN trim_start TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE jobs ADD COLUMN trim_end TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE jobs ADD COLUMN trim_last_secs INTEGER NOT NULL DEFAULT 0`)
	return nil
}

func (d *DB) CreateJob(j *model.Job, submitterIP string) error {
	_, err := d.db.Exec(`
		INSERT INTO jobs (id, url, status, submitter_ip, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		j.ID, j.URL, j.Status, submitterIP, j.CreatedAt, j.UpdatedAt)
	return err
}

// ActiveJobsForIP counts jobs from a specific IP that are not yet finished.
func (d *DB) ActiveJobsForIP(ip string) (int, error) {
	var count int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM jobs
		WHERE submitter_ip = ? AND status IN ('pending', 'queued', 'running', 'ready')`, ip).Scan(&count)
	return count, err
}

// ActiveJobsTotal counts all jobs that are not yet finished.
func (d *DB) ActiveJobsTotal() (int, error) {
	var count int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM jobs
		WHERE status IN ('pending', 'queued', 'running', 'ready')`).Scan(&count)
	return count, err
}

func (d *DB) GetJob(id string) (*model.Job, error) {
	j := &model.Job{}
	err := d.db.QueryRow(`
		SELECT id, url, title, status, mode, video_itag, audio_itag,
		       file_path, file_name, error, worker_id, proxy_note,
		       trim_start, trim_end, trim_last_secs, retries, created_at, updated_at
		FROM jobs WHERE id = ?`, id).Scan(
		&j.ID, &j.URL, &j.Title, &j.Status, &j.Mode,
		&j.VideoItag, &j.AudioItag, &j.FilePath, &j.FileName,
		&j.Error, &j.WorkerID, &j.ProxyNote,
		&j.TrimStart, &j.TrimEnd, &j.TrimLastSecs, &j.Retries, &j.CreatedAt, &j.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

func (d *DB) UpdateJob(j *model.Job) error {
	j.UpdatedAt = time.Now().UTC()
	_, err := d.db.Exec(`
		UPDATE jobs SET title=?, status=?, mode=?, video_itag=?, audio_itag=?,
		       file_path=?, file_name=?, error=?, worker_id=?, proxy_note=?,
		       trim_start=?, trim_end=?, trim_last_secs=?, retries=?, updated_at=?
		WHERE id=?`,
		j.Title, j.Status, j.Mode, j.VideoItag, j.AudioItag,
		j.FilePath, j.FileName, j.Error, j.WorkerID, j.ProxyNote,
		j.TrimStart, j.TrimEnd, j.TrimLastSecs, j.Retries, j.UpdatedAt, j.ID)
	return err
}

// AssignableJob returns one pending or queued job for a worker.
func (d *DB) AssignableJob(workerID string) (*model.Job, error) {
	j := &model.Job{}
	err := d.db.QueryRow(`
		SELECT id, url, title, status, mode, video_itag, audio_itag,
		       file_path, file_name, error, worker_id, proxy_note,
		       trim_start, trim_end, trim_last_secs, retries, created_at, updated_at
		FROM jobs
		WHERE status IN ('pending', 'queued') AND (worker_id = '' OR worker_id = ?)
		ORDER BY created_at ASC
		LIMIT 1`, workerID).Scan(
		&j.ID, &j.URL, &j.Title, &j.Status, &j.Mode,
		&j.VideoItag, &j.AudioItag, &j.FilePath, &j.FileName,
		&j.Error, &j.WorkerID, &j.ProxyNote,
		&j.TrimStart, &j.TrimEnd, &j.TrimLastSecs, &j.Retries, &j.CreatedAt, &j.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

func (d *DB) SaveFormats(jobID string, formats []model.Format) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`DELETE FROM job_formats WHERE job_id = ?`, jobID)
	if err != nil {
		return err
	}
	for _, f := range formats {
		_, err = tx.Exec(`
			INSERT INTO job_formats (job_id, itag, quality, mime_type, container, type, bitrate, width, height, fps, label)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			jobID, f.Itag, f.Quality, f.MimeType, f.Container, f.Type, f.Bitrate, f.Width, f.Height, f.FPS, f.Label)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) GetFormats(jobID string) ([]model.Format, error) {
	rows, err := d.db.Query(`
		SELECT itag, quality, mime_type, container, type, bitrate, width, height, fps, label
		FROM job_formats WHERE job_id = ? ORDER BY bitrate DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var formats []model.Format
	for rows.Next() {
		var f model.Format
		if err := rows.Scan(&f.Itag, &f.Quality, &f.MimeType, &f.Container, &f.Type,
			&f.Bitrate, &f.Width, &f.Height, &f.FPS, &f.Label); err != nil {
			return nil, err
		}
		formats = append(formats, f)
	}
	return formats, rows.Err()
}

// UpsertWorker registers or updates a worker.
func (d *DB) UpsertWorker(w *model.WorkerInfo) error {
	_, err := d.db.Exec(`
		INSERT INTO workers (id, addr, max_jobs, last_seen)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			addr=excluded.addr, max_jobs=excluded.max_jobs, last_seen=excluded.last_seen`,
		w.ID, w.Addr, w.MaxJobs, w.LastSeen)
	return err
}

func (d *DB) WorkerHeartbeat(id string) error {
	_, err := d.db.Exec(`UPDATE workers SET last_seen = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

func (d *DB) CooldownWorker(id string, until time.Time) error {
	_, err := d.db.Exec(`UPDATE workers SET cool_until = ? WHERE id = ?`, until, id)
	return err
}

func (d *DB) GetAvailableWorker(excludeID string) (*model.WorkerInfo, error) {
	w := &model.WorkerInfo{}
	err := d.db.QueryRow(`
		SELECT id, addr, max_jobs, active_jobs, cool_until, last_seen
		FROM workers
		WHERE id != ? AND cool_until < ? AND active_jobs < max_jobs
		  AND last_seen > ?
		ORDER BY active_jobs ASC
		LIMIT 1`, excludeID, time.Now().UTC(), time.Now().UTC().Add(-60*time.Second)).Scan(
		&w.ID, &w.Addr, &w.MaxJobs, &w.ActiveJobs, &w.CoolUntil, &w.LastSeen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return w, err
}

// GetFormatsJSON returns formats as a JSON string for embedding in templates.
func (d *DB) GetFormatsJSON(jobID string) (string, error) {
	formats, err := d.GetFormats(jobID)
	if err != nil {
		return "[]", err
	}
	b, err := json.Marshal(formats)
	if err != nil {
		return "[]", err
	}
	return string(b), nil
}

// ExpiredJobs returns jobs older than the given duration.
func (d *DB) ExpiredJobs(maxAge time.Duration) ([]model.Job, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	rows, err := d.db.Query(`
		SELECT id, file_path FROM jobs WHERE created_at < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []model.Job
	for rows.Next() {
		var j model.Job
		if err := rows.Scan(&j.ID, &j.FilePath); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// DeleteJob removes a job and its formats from the database.
func (d *DB) DeleteJob(id string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tx.Exec(`DELETE FROM job_formats WHERE job_id = ?`, id)
	tx.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	return tx.Commit()
}
