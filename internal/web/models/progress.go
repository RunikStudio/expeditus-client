package models

import "time"

const (
	StageLogin      = "login"
	StageNavigation = "navigation"
	StageScraping   = "scraping"
	StageProcessing = "processing"
	StageComplete   = "complete"
)

type ProgressUpdate struct {
	SessionID  string    `json:"sessionId"`
	Stage      string    `json:"stage"`
	Progress   float64   `json:"progress"`
	TotalItems int       `json:"totalItems"`
	Processed  int       `json:"processed"`
	Speed      string    `json:"speed"`
	ETA        string    `json:"eta"`
	Timestamp  time.Time `json:"timestamp"`
}

func NewProgressUpdate(sessionID string) *ProgressUpdate {
	return &ProgressUpdate{
		SessionID: sessionID,
		Stage:     StageLogin,
		Progress:  0,
		Timestamp: time.Now(),
	}
}

func (p *ProgressUpdate) SetStage(stage string) {
	p.Stage = stage
}

func (p *ProgressUpdate) SetProgress(progress float64) {
	p.Progress = progress
}

func (p *ProgressUpdate) UpdateProcessed(processed int, total int) {
	p.Processed = processed
	p.TotalItems = total
	if total > 0 {
		p.Progress = float64(processed) / float64(total) * 100
	}
}

func (p *ProgressUpdate) SetSpeed(speed string) {
	p.Speed = speed
}

func (p *ProgressUpdate) SetETA(eta string) {
	p.ETA = eta
}
