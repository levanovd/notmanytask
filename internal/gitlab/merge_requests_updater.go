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
	Open         *models.MergeRequest
	Merged       *models.MergeRequest
	HasUserNotes bool
}

func NewMergeRequestsUpdater(client *Client, db *database.DataBase) (*MergeRequestsUpdater, error) {
	return &MergeRequestsUpdater{
		Client: client,
		logger: client.logger.Named("merge_requests_updater"),
		db:     db,
	}, nil
}

func (p MergeRequestsUpdater) Run(ctx context.Context) {
	tick := time.Tick(p.config.PullIntervals.MergeRequestsUpdater)

	for {
		select {
		case <-tick:
			p.updateMergeRequests()
		case <-ctx.Done():
			p.logger.Info("Stopping merge requests updater")
			return
		}
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
				p.logger.Error("Failed to list branches", zap.Error(err), lf.ProjectName(project.Name))
				return err
			}

			for _, branch := range branches {
				p.logger.Info("Found branch", lf.ProjectName(project.Name), lf.BranchName(branch.Name))
				if !IsSubmitBranch(branch.Name) {
					continue
				}
				task := ParseTaskFromBranch(branch.Name)

				p.manageGitlabMergeRequests(project, branch, task, reviewMergeRequestDeadline)
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
	p.logger.Info("Managing MRs at gitlab", lf.ProjectName(project.Name), lf.BranchName(branch.Name))

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

			if mergeRequests.Open.MergeStatus != models.MergeRequestStatusCannotBeMerged &&
				!mergeRequests.HasUserNotes &&
				!mergeRequests.Open.ExtraChanges &&
				mergeRequests.Open.LastPipelineCreatedAt.Before(reviewDeadline) &&
				mergeRequests.Open.LastPipelineStatus == models.PipelineStatusSuccess {
				mergeCommitMessage := "Automatic merge"
				options := &gitlab.AcceptMergeRequestOptions{
					MergeCommitMessage: &mergeCommitMessage,
				}
				_, _, err = p.gitlab.MergeRequests.AcceptMergeRequest(project.ID, mergeRequests.Open.IID, options)
				if err != nil {
					p.logger.Error("Failed to accept merge request", zap.Error(err), lf.ProjectName(project.Name), lf.BranchName(branch.Name))
					return
				}
				p.logger.Info("Accepted merge request", lf.ProjectName(project.Name), lf.BranchName(task))
			}
		}
	} else {
		p.logger.Info("Has merged merge requests, will do nothing", lf.ProjectName(project.Name), lf.BranchName(branch.Name))
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
	result := &branchMergeRequests{}

	mergeRequests, err := p.db.ListProjectBranchMergeRequests(project.Name, ParseTaskFromBranch(branch.Name))
	if err != nil {
		p.logger.Error("Failed to list branch merge requests", zap.Error(err), lf.ProjectName(project.Name), lf.BranchName(branch.Name))
		return nil, err
	}

	for _, mr := range mergeRequests {
		if mr.State == "opened" {
			if result.Open == nil || result.Open.StartedAt.Before(mr.StartedAt) {
				result.Open = mr
			}
		} else if mr.State == models.MergeRequestStateMerged {
			if result.Merged == nil || result.Merged.StartedAt.Before(mr.StartedAt) {
				result.Merged = mr
			}
		}
		if mr.UserNotesCount > 0 {
			result.HasUserNotes = true
		}
	}

	return result, nil
}
