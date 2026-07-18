package models

// BranchName identifies a git branch.
type BranchName string

// CommitSHA identifies a git commit.
type CommitSHA string

// BranchState records whether a run branched off a default branch to keep its
// commits isolated, and if so which branch it created and the commit that
// branch started from — the squash target once execution completes.
type BranchState struct {
	Created bool
	Name    BranchName
	Base    CommitSHA
}
