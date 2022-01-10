package gitlab

import (
	"context"
	"time"

	"github.com/xanzy/go-gitlab"
	"go.uber.org/zap"

	"github.com/bigredeye/notmanytask/internal/database"
	lf "github.com/bigredeye/notmanytask/internal/logfield"
	"github.com/bigredeye/notmanytask/internal/models"
)

type MergeRequestsUpdater struct {
	*Client

	logger *zap.Logger
	db     *database.DataBase
}

func NewMergeRequestsUpdater(client *Client, db *database.DataBase) (*MergeRequestsUpdater, error) {
	return &MergeRequestsUpdater{
		Client: client,
		logger: client.logger.Named("merge_requests"),
		db:     db,
	}, nil
}

func (p MergeRequestsUpdater) Run(ctx context.Context) {
	tick := time.Tick(p.config.PullIntervals.MergeRequests)

	for {
		select {
		case <-tick:
			p.updateMergeRequests()
		case <-ctx.Done():
			p.logger.Info("Stopping merge requests fetcher")
			return
		}
	}
}

func (p MergeRequestsUpdater) addMergeRequest(projectName string, mergeRequest *gitlab.MergeRequest) error {
	return p.db.AddMergeRequest(&models.MergeRequest{
		ID:        mergeRequest.ID,
		Task:      ParseTaskFromBranch(mergeRequest.SourceBranch),
		Status:    getMergeRequestState(mergeRequest),
		Project:   projectName,
		StartedAt: *mergeRequest.CreatedAt,
		IID:       mergeRequest.IID,
	})
}

func getMergeRequestState(mergeRequest *gitlab.MergeRequest) models.MergeRequestStatus {
	if mergeRequest.State == "merged" {
		return models.MergeRequestMerged
	} else if mergeRequest.UserNotesCount > 0 {
		return models.MergeRequestOnReview
	} else {
		return models.MergeRequestPending
	}
}

func (p MergeRequestsUpdater) updateMergeRequests() {
	p.logger.Info("Start merge requests creator iteration")
	defer p.logger.Info("Finish merge requests creator iteration")

	reviewMergeRequestDeadline := time.Now().Add(-p.config.GitLab.ReviewTtl)

	err := p.ForEachProject(func(project *gitlab.Project) error {
		p.logger.Info("Found project", lf.ProjectName(project.Name))
		options := &gitlab.ListBranchesOptions{}
		for {
			branches, resp, err := p.gitlab.Branches.ListBranches(project.ID, options)
			if err != nil {
				p.logger.Error("Failed to list branches", zap.Error(err))
				return err
			}

			for _, branch := range branches {
				p.logger.Info("Found branch", lf.ProjectName(project.Name), lf.BranchName(branch.Name))
				if !IsSubmitBranch(branch.Name) {
					continue
				}
				task := ParseTaskFromBranch(branch.Name)
				mergeRequest, err := p.db.FindMergeRequest(project.Name, task)
				if err != nil {
					p.logger.Error("Failed to find merge request", zap.Error(err))
					return err
				}
				if mergeRequest != nil {
					p.logger.Info("Found merge request", lf.ProjectName(project.Name), lf.BranchName(branch.Name))
					err = p.updateMergeRequest(project.ID, mergeRequest, reviewMergeRequestDeadline)
					if err != nil {
						p.logger.Error("Failed to update merge request", zap.Error(err))
						return err
					}
					continue
				}
				mergeRequestCreated, err := p.createMergeRequest(project.ID, branch.Name)
				if err != nil {
					p.logger.Error("Failed to create merge request", zap.Error(err), lf.ProjectName(project.Name), lf.BranchName(branch.Name))
				}
				if err = p.addMergeRequest(project.Name, mergeRequestCreated); err != nil {
					p.logger.Error("Failed to add merge request", zap.Error(err), lf.ProjectName(project.Name), lf.MergeRequestID(mergeRequest.ID))
				}
			}

			if resp.CurrentPage >= resp.TotalPages {
				break
			}
			options.Page = resp.NextPage
		}

		return nil
	})

	if err == nil {
		p.logger.Info("Successfully update merge requests")
	} else {
		p.logger.Error("Failed to update merge requests", zap.Error(err))
	}
}

func (p MergeRequestsUpdater) createMergeRequest(project int, branch string) (*gitlab.MergeRequest, error) {
	main := "main"
	options := &gitlab.CreateMergeRequestOptions{
		SourceBranch: &branch,
		TargetBranch: &main,
		Title:        &branch,
	}
	mergeRequest, _, err := p.gitlab.MergeRequests.CreateMergeRequest(project, options)
	if err != nil {
		return nil, err
	}

	return mergeRequest, nil
}

func (p MergeRequestsUpdater) updateMergeRequest(project int, mergeRequest *models.MergeRequest, reviewDeadline time.Time) error {
	options := &gitlab.GetMergeRequestsOptions{}
	gitlabMergeRequest, _, err := p.gitlab.MergeRequests.GetMergeRequest(project, mergeRequest.IID, options)
	if err != nil {
		p.logger.Error("Failed to get merge request", zap.Error(err))
		return err
	}
	p.logger.Info("Got merge request from gitlab", lf.ProjectName(mergeRequest.Project), lf.BranchName(mergeRequest.Task))

	pipeline, err := p.db.FindLatestPipeline(mergeRequest.Project, mergeRequest.Task)
	if err != nil {
		p.logger.Error("Failed to find latest pipeline for merge request", zap.Error(err))
		return err
	}
	p.logger.Info("Found latest pipeline", lf.ProjectName(mergeRequest.Project), lf.BranchName(mergeRequest.Task))

	if gitlabMergeRequest.MergeStatus == "can_be_merged" &&
		gitlabMergeRequest.UserNotesCount == 0 &&
		pipeline.StartedAt.After(reviewDeadline) &&
		pipeline.Status == models.PipelineStatusSuccess {
		mergeCommitMessage := "Automatic merge"
		options := &gitlab.AcceptMergeRequestOptions{
			MergeCommitMessage: &mergeCommitMessage,
		}
		_, _, err = p.gitlab.MergeRequests.AcceptMergeRequest(project, mergeRequest.IID, options)
		if err != nil {
			p.logger.Error("Failed to accept merge request", zap.Error(err))
			return err
		}
		p.logger.Info("Accepted merge request", lf.ProjectName(mergeRequest.Project), lf.BranchName(mergeRequest.Task))
		mergeRequest.Status = models.MergeRequestMerged
	} else {
		mergeRequest.Status = getMergeRequestState(gitlabMergeRequest)
	}

	err = p.db.AddMergeRequest(mergeRequest)
	if err != nil {
		p.logger.Error("Failed to update merge request in db", zap.Error(err))
		return err
	}
	return nil
}
