package controller

import (
	"log"
	"time"

	"youdl/internal/model"
)

const (
	maxRetries      = 3
	cooldownSeconds = 60
)

// AssignJob finds the next assignable job for a worker and claims it.
func (s *Server) AssignJob(workerID string) (*model.Job, error) {
	job, err := s.db.AssignableJob(workerID)
	if err != nil || job == nil {
		return nil, err
	}

	job.WorkerID = workerID
	// pending → stays pending (worker will fetch metadata)
	// queued → running (worker will download)
	if job.Status == model.StatusQueued {
		job.Status = model.StatusRunning
	}
	if err := s.db.UpdateJob(job); err != nil {
		return nil, err
	}
	return job, nil
}

// HandleJobFailure implements retry with worker rotation.
func (s *Server) HandleJobFailure(jobID, workerID, errMsg string) error {
	job, err := s.db.GetJob(jobID)
	if err != nil || job == nil {
		return err
	}

	job.Retries++
	if job.Retries >= maxRetries {
		job.Status = model.StatusFailed
		job.Error = errMsg
		return s.db.UpdateJob(job)
	}

	// Cool down the failing worker
	coolUntil := time.Now().UTC().Add(cooldownSeconds * time.Second)
	if err := s.db.CooldownWorker(workerID, coolUntil); err != nil {
		log.Printf("cooldown worker %s: %v", workerID, err)
	}

	// Reset job for reassignment
	job.WorkerID = ""
	if job.Mode != "" {
		job.Status = model.StatusQueued
	} else {
		job.Status = model.StatusPending
	}
	job.Error = errMsg
	return s.db.UpdateJob(job)
}
