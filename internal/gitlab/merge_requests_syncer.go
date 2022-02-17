package gitlab

import (
	"context"
	"fmt"
	"github.com/bigredeye/notmanytask/internal/database"
	lf "github.com/bigredeye/notmanytask/internal/logfield"
	"github.com/bigredeye/notmanytask/internal/models"
	"github.com/xanzy/go-gitlab"
	"go.uber.org/zap"
	"strings"
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

type notesInfo struct {
	HasUnresolvedNotes bool
	LastNoteCreatedAt  time.Time
	LastNoteResolvedAt time.Time
}

func (p MergeRequestsSyncer) getNotesInfo(project *gitlab.Project, mergeRequest *gitlab.MergeRequest) (notesInfo, error) {
	createdAt := "created_at"
	desc := "desc"
	options := &gitlab.ListMergeRequestNotesOptions{
		OrderBy: &createdAt,
		Sort:    &desc,
	}

	result := notesInfo{}

	for {
		notes, response, err := p.gitlab.Notes.ListMergeRequestNotes(project.ID, mergeRequest.IID, options)

		if err != nil {
			return result, err
		}

		for _, note := range notes {
			if note.Resolvable {
				if time.Time.IsZero(result.LastNoteCreatedAt) {
					result.LastNoteCreatedAt = *note.CreatedAt
				}
				if !note.Resolved {
					result.HasUnresolvedNotes = true
					return result, nil
				} else if note.ResolvedAt.After(result.LastNoteResolvedAt) {
					result.LastNoteResolvedAt = *note.ResolvedAt
				}
			}
		}

		if response.CurrentPage >= response.TotalPages {
			break
		}
		options.Page = response.NextPage
	}

	return result, nil
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

	notesInfo, err := p.getNotesInfo(project, mr)
	if err != nil {
		p.logger.Error("Failed to list notes for MR", zap.Error(err), lf.ProjectName(project.Name), lf.BranchName(mr.SourceBranch))
		return err
	}

	extraChanges, err := p.checkForExtraChanges(project, mr, task)

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
		HasUnresolvedNotes: notesInfo.HasUnresolvedNotes,
		LastNoteCreatedAt:  notesInfo.LastNoteCreatedAt,
		LastNoteResolvedAt: notesInfo.LastNoteResolvedAt,
		ExtraChanges:       extraChanges,
	})

	if err != nil {
		p.logger.Error("Failed to add MR to DB", zap.Error(err), lf.ProjectName(project.Name), lf.BranchName(mr.SourceBranch))
		return err
	}
	p.logger.Info("Added MR to DB", lf.ProjectName(project.Name), lf.BranchName(mr.SourceBranch))
	return nil
}

func (p MergeRequestsSyncer) checkForExtraChanges(project *gitlab.Project, mergeRequest *gitlab.MergeRequest, task string) (bool, error) {
	options := &gitlab.GetMergeRequestChangesOptions{}

	allowedPrefix := fmt.Sprintf("tasks/%s/", task)

	mr, _, err := p.gitlab.MergeRequests.GetMergeRequestChanges(project.ID, mergeRequest.IID, options)

	if err != nil {
		return false, err
	}

	for _, change := range mr.Changes {
		if !strings.HasPrefix(change.NewPath, allowedPrefix) || !strings.HasPrefix(change.OldPath, allowedPrefix) {
			return true, nil
		}
	}

	return false, nil
}
