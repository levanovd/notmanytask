package models

import (
	"time"
)

const (
	MergeRequestStateMerged = "merged"
	MergeRequestStateClosed = "closed"

	MergeRequestStatusCannotBeMerged = "cannot_be_merged"
)

type MergeRequest struct {
	ID      int    `gorm:"primaryKey"`
	Project string `gorm:"index"`
	Task    string `gorm:"index"`

	State              string
	UserNotesCount     int
	StartedAt          time.Time
	MergeStatus        string
	IID                int
	MergeUserLogin     string
	HasUnresolvedNotes bool
}
