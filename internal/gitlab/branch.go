package gitlab

import "strings"

const (
	branchPrefix = "submits/"
)

func ParseTaskFromBranch(task string) string {
	return strings.TrimPrefix(task, branchPrefix)
}

func MakeBranchForTask(task string) string {
	return branchPrefix + task
}
