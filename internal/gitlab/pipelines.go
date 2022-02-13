package gitlab

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xanzy/go-gitlab"
	"go.uber.org/zap"

	"github.com/bigredeye/notmanytask/internal/database"
	lf "github.com/bigredeye/notmanytask/internal/logfield"
	"github.com/bigredeye/notmanytask/internal/models"
)

type PipelinesFetcher struct {
	*Client

	logger *zap.Logger
	db     *database.DataBase
}

func NewPipelinesFetcher(client *Client, db *database.DataBase) (*PipelinesFetcher, error) {
	return &PipelinesFetcher{
		Client: client,
		logger: client.logger.Named("pipelines"),
		db:     db,
	}, nil
}

func (p PipelinesFetcher) Run(ctx context.Context) {
	tick := time.Tick(p.config.PullIntervals.Pipelines)

	for {
		select {
		case <-tick:
			p.fetchAllPipelines()
		case <-ctx.Done():
			p.logger.Info("Stopping pipelines fetcher")
			return
		}
	}
}

func (p PipelinesFetcher) Fetch(id int, project string) error {
	log := p.logger.With(
		lf.PipelineID(id),
		lf.ProjectName(project),
	)

	log.Info("Fetching pipeline")

	pipeline, _, err := p.gitlab.Pipelines.GetPipeline(p.Client.MakeProjectWithNamespace(project), id)
	if err != nil {
		log.Error("Failed to fetch pipeline", zap.Error(err))
		return errors.Wrap(err, "Failed to fetch pipeline")
	}

	return p.addPipeline(project, &gitlab.PipelineInfo{
		ID:        pipeline.ID,
		Ref:       pipeline.Ref,
		Status:    pipeline.Status,
		CreatedAt: pipeline.CreatedAt,
		ProjectID: pipeline.ProjectID,
	})
}

func (p PipelinesFetcher) addPipeline(projectName string, pipeline *gitlab.PipelineInfo) error {
	return p.db.AddPipeline(&models.Pipeline{
		ID:        pipeline.ID,
		Task:      ParseTaskFromBranch(pipeline.Ref),
		Status:    pipeline.Status,
		Project:   projectName,
		StartedAt: *pipeline.CreatedAt,
	})
}

func (p PipelinesFetcher) fetchAllPipelines() {
	p.logger.Info("Start pipelines fetcher iteration")
	defer p.logger.Info("Finish pipelines fetcher iteration")

	err := p.ForEachProject(func(project *gitlab.Project) error {
		p.logger.Info("Found project", lf.ProjectName(project.Name))
		options := &gitlab.ListProjectPipelinesOptions{}
		mergedTasks, err := p.db.GetTasksWithMergedRequests(project.Name)
		if err != nil {
			p.logger.Error("Failed to list merged tasks", zap.Error(err), lf.ProjectName(project.Name))
			return err
		}

		for {
			pipelines, resp, err := p.gitlab.Pipelines.ListProjectPipelines(project.ID, options)
			if err != nil {
				p.logger.Error("Failed to list projects", zap.Error(err))
				return err
			}

			for _, pipeline := range pipelines {
				if mergedTasks[ParseTaskFromBranch(pipeline.Ref)] {
					continue
				}
				p.logger.Info("Found pipeline", lf.ProjectName(project.Name), lf.PipelineID(pipeline.ID), lf.PipelineStatus(pipeline.Status))
				if err = p.addPipeline(project.Name, pipeline); err != nil {
					p.logger.Error("Failed to add pipeline", zap.Error(err), lf.ProjectName(project.Name), lf.PipelineID(pipeline.ID))
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
		p.logger.Info("Sucessfully fetched pipelines")
	} else {
		p.logger.Error("Failed to fetch pipelines", zap.Error(err))
	}
}
