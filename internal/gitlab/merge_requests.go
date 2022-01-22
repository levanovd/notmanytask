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

type branchMergeRequests struct {
	Open   *gitlab.MergeRequest
	Merged *gitlab.MergeRequest
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

				p.manageGitlabMergeRequests(project, branch, task, reviewMergeRequestDeadline)
				p.syncDbMergeRequests(project, branch, task)
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

func (p MergeRequestsUpdater) manageGitlabMergeRequests(project *gitlab.Project, branch *gitlab.Branch, task string, reviewDeadline time.Time) {
	mergeRequests, err := p.getBranchMergeRequests(project, branch)
	if err != nil {
		return
	}

	if mergeRequests.Merged == nil {
		if mergeRequests.Open == nil {
			p.logger.Info("Have no merged or open MRs, creating a new one", lf.ProjectName(project.Name), lf.BranchName(branch.Name))
			_, err = p.createMergeRequest(project.ID, branch.Name)
			if err != nil {
				p.logger.Error("Failed to create merge request", zap.Error(err), lf.ProjectName(project.Name), lf.BranchName(branch.Name))
				return
			}
		} else {
			p.logger.Info("Got an open merge request from gitlab", lf.ProjectName(project.Name), lf.BranchName(branch.Name))

			pipeline, err := p.db.FindLatestPipeline(project.Name, task)
			if err != nil {
				p.logger.Error("Failed to find latest pipeline for merge request", zap.Error(err))
				return
			}
			p.logger.Info("Found latest pipeline", lf.ProjectName(project.Name), lf.BranchName(branch.Name))

			if mergeRequests.Open.MergeStatus == "can_be_merged" &&
				mergeRequests.Open.UserNotesCount == 0 &&
				pipeline.StartedAt.Before(reviewDeadline) &&
				pipeline.Status == models.PipelineStatusSuccess {
				mergeCommitMessage := "Automatic merge"
				options := &gitlab.AcceptMergeRequestOptions{
					MergeCommitMessage: &mergeCommitMessage,
				}
				_, _, err = p.gitlab.MergeRequests.AcceptMergeRequest(project, mergeRequests.Open.IID, options)
				if err != nil {
					p.logger.Error("Failed to accept merge request", zap.Error(err))
					return
				}
				p.logger.Info("Accepted merge request", lf.ProjectName(project.Name), lf.BranchName(task))
			}
		}
	} else {
		p.logger.Info("Has closed merge requests, will do nothing", lf.ProjectName(project.Name), lf.BranchName(branch.Name))
	}
}

func (p MergeRequestsUpdater) syncDbMergeRequests(project *gitlab.Project, branch *gitlab.Branch, task string) {
	mergeRequests, err := p.getBranchMergeRequests(project, branch)
	if err != nil {
		return
	}

	var mr *gitlab.MergeRequest
	if mergeRequests.Open != nil {
		mr = mergeRequests.Open
		p.logger.Info("Found an open merge request", lf.ProjectName(project.Name), lf.BranchName(branch.Name))
	}
	if mergeRequests.Merged != nil {
		mr = mergeRequests.Merged
		p.logger.Info("Found a merged merge request", lf.ProjectName(project.Name), lf.BranchName(branch.Name))
	}

	err = p.db.AddMergeRequest(&models.MergeRequest{
		Task:      task,
		Status:    getMergeRequestState(mr),
		Project:   project.Name,
		StartedAt: *mr.CreatedAt,
		IID:       mr.IID,
	})
	if err != nil {
		p.logger.Error("Failed to update merge request in db", zap.Error(err))
		return
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

func (p MergeRequestsUpdater) getBranchMergeRequests(project *gitlab.Project, branch *gitlab.Branch) (*branchMergeRequests, error) {
	result := branchMergeRequests{}

	options := &gitlab.ListProjectMergeRequestsOptions{
		SourceBranch: &branch.Name,
	}

	gitlabMergeRequests, _, err := p.gitlab.MergeRequests.ListProjectMergeRequests(project.ID, options)
	if err != nil {
		p.logger.Error("Failed to get branch merge requests", zap.Error(err))
		return nil, err
	}

	for _, mr := range gitlabMergeRequests {
		if mr.State == "opened" {
			if result.Open == nil || result.Open.CreatedAt.Before(*mr.CreatedAt) {
				result.Open = mr
			}
		} else if mr.State == "merged" {
			if result.Merged == nil || result.Merged.CreatedAt.Before(*mr.CreatedAt) {
				result.Merged = mr
			}
		}
	}

	return &result, nil
}
