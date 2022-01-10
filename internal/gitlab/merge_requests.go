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

type MergeRequestsFetcher struct {
	*Client

	logger *zap.Logger
	db     *database.DataBase
}

func NewMergeRequestsFetcher(client *Client, db *database.DataBase) (*MergeRequestsFetcher, error) {
	return &MergeRequestsFetcher{
		Client: client,
		logger: client.logger.Named("merge_requests"),
		db:     db,
	}, nil
}

func (p MergeRequestsFetcher) Run(ctx context.Context) {
	tick := time.Tick(p.config.PullIntervals.MergeRequests)

	for {
		select {
		case <-tick:
			p.fetchAllMergeRequests()
		case <-ctx.Done():
			p.logger.Info("Stopping merge requests fetcher")
			return
		}
	}
}

func (p MergeRequestsFetcher) addMergeRequest(projectName string, mergeRequest *gitlab.MergeRequest) error {
	return p.db.AddMergeRequest(&models.MergeRequest{
		ID:        mergeRequest.ID,
		Task:      ParseTaskFromBranch(mergeRequest.SourceBranch),
		Status:    getMergeRequestState(mergeRequest),
		Project:   projectName,
		StartedAt: *mergeRequest.CreatedAt,
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

func (p MergeRequestsFetcher) fetchAllMergeRequests() {
	p.logger.Info("Start merge requests fetcher iteration")
	defer p.logger.Info("Finish merge requests fetcher iteration")

	err := p.ForEachProject(func(project *gitlab.Project) error {
		p.logger.Info("Found project", lf.ProjectName(project.Name))
		options := &gitlab.ListProjectMergeRequestsOptions{}
		for {
			mergeRequests, resp, err := p.gitlab.MergeRequests.ListProjectMergeRequests(project.ID, options)
			if err != nil {
				p.logger.Error("Failed to list merge reqiests", zap.Error(err))
				return err
			}

			for _, mergeRequest := range mergeRequests {
				p.logger.Info("Found merge request", lf.ProjectName(project.Name), lf.PipelineID(mergeRequest.ID), lf.PipelineStatus(mergeRequest.State))
				if err = p.addMergeRequest(project.Name, mergeRequest); err != nil {
					p.logger.Error("Failed to add merge request", zap.Error(err), lf.ProjectName(project.Name), lf.PipelineID(mergeRequest.ID))
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
		p.logger.Info("Sucessfully fetched merge requests")
	} else {
		p.logger.Error("Failed to fetch merge requests", zap.Error(err))
	}
}
