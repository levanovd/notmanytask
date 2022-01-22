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
	Project string `gorm:"primaryKey"`

	Task      string `gorm:"primaryKey"`
	Status    MergeRequestStatus
	StartedAt time.Time
	IID       int
}
