package models

import (
	"time"
)

const (
	MergeRequestOnReview = "on_review"
	MergeRequestPending  = "pending"
	MergeRequestMerged   = "merged"
)

type MergeRequestStatus = string

type MergeRequest struct {
	ID      int    `gorm:"primaryKey"`
	Project string `gorm:"index"`
	Task    string `gorm:"index"`

	State          string
	UserNotesCount int
	StartedAt      time.Time
	MergeStatus    string
	IID            int
}
