package main

import (
	"fmt"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type Repository struct {
	Repo *git.Repository
}

func main() {
}

func NewRepository() (*Repository, error) {
	repoRoot := filepath.Join("..", "..")
	repo, err := git.PlainOpen(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}
	return &Repository{Repo: repo}, nil
}

func (r *Repository) MostRecentTag() (string, error) {
	tagMap, err := r.tags()
	if err != nil {
		return "", fmt.Errorf("failed to get tags: %w", err)
	}

	head, err := r.Repo.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	history, err := r.Repo.Log(&git.LogOptions{
		From:  head.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get commit log: %w", err)
	}

	var latestTag string
	for latestTag == "" {
		commit, err := history.Next()
		if err != nil {
			return "", fmt.Errorf("failed to get next commit: %w", err)
		}
		if tag, ok := tagMap[commit.Hash.String()]; ok {
			latestTag = tag
		}
	}

	if latestTag == "" {
		return "", fmt.Errorf("no tag found in history of HEAD")
	}
	return latestTag, nil
}

func (r *Repository) tags() (map[string]string, error) {
	tagMap := make(map[string]string)
	tags, err := r.Repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("failed to get tags: %w", err)
	}
	if err := tags.ForEach(func(ref *plumbing.Reference) error {
		tagMap[ref.Hash().String()] = ref.Name().Short()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to iterate tags: %w", err)
	}

	return tagMap, nil
}
