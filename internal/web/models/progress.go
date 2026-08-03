package models

import "time"

const (
	StageLogin       = "login"
	StageTwoFA       = "2fa"
	StageNavigation  = "navigation"
	StageScraping    = "scraping"
	StageProcessing  = "processing"
	StageComplete    = "complete"
	StageSearching   = "searching"
)

type ProgressUpdate struct {
	SessionID     string    `json:"sessionId"`
	Stage         string    `json:"stage"`
	Progress      float64   `json:"progress"`
	TotalItems    int       `json:"totalItems"`
	Processed     int       `json:"processed"`
	Speed         string    `json:"speed"`
	ETA           string    `json:"eta"`
	Timestamp     time.Time `json:"timestamp"`
	CurrentAction string    `json:"currentAction"`
	CurrentHotel  string    `json:"currentHotel"`
	ElapsedTime   string    `json:"elapsedTime"`
	RoomsFound    int       `json:"roomsFound"`
	PricesFound   int       `json:"pricesFound"`
	Status        string    `json:"status"`
}

func NewProgressUpdate(sessionID string) *ProgressUpdate {
	return &ProgressUpdate{
		SessionID: sessionID,
		Stage:     StageLogin,
		Progress:  0,
		Timestamp: time.Now(),
		Status:    "running",
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

func (p *ProgressUpdate) SetCurrentAction(action string) {
	p.CurrentAction = action
}

func (p *ProgressUpdate) SetCurrentHotel(hotel string) {
	p.CurrentHotel = hotel
}

func (p *ProgressUpdate) SetElapsedTime(elapsed string) {
	p.ElapsedTime = elapsed
}

func (p *ProgressUpdate) SetRoomsFound(rooms int) {
	p.RoomsFound = rooms
}

func (p *ProgressUpdate) SetPricesFound(prices int) {
	p.PricesFound = prices
}

func (p *ProgressUpdate) SetStatus(status string) {
	p.Status = status
}
