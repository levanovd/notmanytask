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

	Task      string `gorm:"index"`
	Status    MergeRequestStatus
	StartedAt time.Time
}
