package gitlab

import (
	"context"
	"github.com/bigredeye/notmanytask/internal/database"
	lf "github.com/bigredeye/notmanytask/internal/logfield"
	"github.com/bigredeye/notmanytask/internal/models"
	"github.com/xanzy/go-gitlab"
	"go.uber.org/zap"
	"time"
)

type MergeRequestsSyncer struct {
	*Client

	logger *zap.Logger
	db     *database.DataBase
}

func NewMergeRequestsSyncer(client *Client, db *database.DataBase) (*MergeRequestsSyncer, error) {
	return &MergeRequestsSyncer{
		Client: client,
		logger: client.logger.Named("merge_requests_syncer"),
		db:     db,
	}, nil
}

func (p MergeRequestsSyncer) Run(ctx context.Context) {
	tick := time.Tick(p.config.PullIntervals.MergeRequestsSyncer)

	for {
		select {
		case <-tick:
			p.syncDbMergeRequests()
		case <-ctx.Done():
			p.logger.Info("Stopping merge requests syncer")
			return
		}
	}
}

func (p MergeRequestsSyncer) syncDbMergeRequests() {
	p.logger.Info("Start merge requests sync iteration")
	defer p.logger.Info("Finish merge requests sync iteration")

	err := p.ForEachProject(func(project *gitlab.Project) error {
		p.logger.Info("Found project", lf.ProjectName(project.Name))
		main := "main"
		withMergeStatusRecheck := true

		options := &gitlab.ListProjectMergeRequestsOptions{
			TargetBranch:           &main,
			WithMergeStatusRecheck: &withMergeStatusRecheck,
		}

		mergedTasks, err := p.db.GetTasksWithMergedRequests(project.Name)
		if err != nil {
			p.logger.Error("Failed to list merged tasks", zap.Error(err), lf.ProjectName(project.Name))
			return err
		}

		for {

			gitlabMergeRequests, response, err := p.gitlab.MergeRequests.ListProjectMergeRequests(project.ID, options)
			if err != nil {
				p.logger.Error("Failed to get merge requests", zap.Error(err), lf.ProjectName(project.Name))
				return err
			}

			for _, mr := range gitlabMergeRequests {
				err = p.addMergeRequest(project, mr, mergedTasks)
				if err != nil {
					return err
				}
			}

			if response.CurrentPage >= response.TotalPages {
				break
			}
			options.Page = response.NextPage
		}
		return nil
	})

	if err == nil {
		p.logger.Info("Successfully synced merge requests")
	} else {
		p.logger.Error("Failed to sync merge requests", zap.Error(err))
	}
}

func (p MergeRequestsSyncer) hasUnresolvedNotes(project *gitlab.Project, mergeRequest *gitlab.MergeRequest) (bool, error) {
	options := &gitlab.ListMergeRequestNotesOptions{}
	for {
		notes, response, err := p.gitlab.Notes.ListMergeRequestNotes(project.ID, mergeRequest.IID, options)

		if err != nil {
			return false, err
		}

		for _, note := range notes {
			if note.Resolvable && !note.Resolved {
				return true, nil
			}
		}

		if response.CurrentPage >= response.TotalPages {
			break
		}
		options.Page = response.NextPage
	}

	return false, nil
}

func (p MergeRequestsSyncer) addMergeRequest(project *gitlab.Project, mr *gitlab.MergeRequest, mergedTasks database.MergedTasks) error {
	if !IsSubmitBranch(mr.SourceBranch) {
		return nil
	}
	task := ParseTaskFromBranch(mr.SourceBranch)
	if mergedTasks[task] {
		return nil
	}
	p.logger.Info("Found MR from branch", lf.ProjectName(project.Name), lf.BranchName(mr.SourceBranch))

	mergeUserLogin := ""
	if mr.MergedBy != nil {
		mergeUserLogin = mr.MergedBy.Username
	}

	hasUnresolvedNotes, err := p.hasUnresolvedNotes(project, mr)
	if err != nil {
		p.logger.Error("Failed to list notes for MR", zap.Error(err), lf.ProjectName(project.Name), lf.BranchName(mr.SourceBranch))
		return err
	}

	err = p.db.AddMergeRequest(&models.MergeRequest{
		ID:                 mr.ID,
		Task:               task,
		Project:            project.Name,
		State:              mr.State,
		UserNotesCount:     mr.UserNotesCount,
		StartedAt:          *mr.CreatedAt,
		IID:                mr.IID,
		MergeStatus:        mr.MergeStatus,
		MergeUserLogin:     mergeUserLogin,
		HasUnresolvedNotes: hasUnresolvedNotes,
	})

	if err != nil {
		p.logger.Error("Failed to add MR to DB", zap.Error(err), lf.ProjectName(project.Name), lf.BranchName(mr.SourceBranch))
		return err
	}
	p.logger.Info("Added MR to DB", lf.ProjectName(project.Name), lf.BranchName(mr.SourceBranch))
	return nil
}
