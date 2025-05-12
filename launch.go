package sqlreplayer

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/luijianfie/sqlreplayer/model"
	"github.com/luijianfie/sqlreplayer/utils"
)

func LanuchAnalyzeTask(jobSeq uint64, c *model.Config) (*SQLReplayer, error) {
	sr, err := NewSQLReplayer(jobSeq, c)
	if err != nil {
		return nil, err
	}

	var expandedFiles []string
	for _, pattern := range c.FileList {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid file pattern: %s, error: %v", pattern, err)
		}

		if len(matches) == 0 {
			sr.logger.Sugar().Warnf("no files match pattern: %s", pattern)
			continue
		}

		for _, f := range matches {
			if !utils.FileExists(f) {
				return nil, fmt.Errorf("file not exist: %s", f)
			}
			expandedFiles = append(expandedFiles, f)
		}
	}

	if len(expandedFiles) == 0 {
		return nil, errors.New("no valid files found matching the patterns")
	}

	sr.logger.Sugar().Infoln("task begin")

	go sr.Start()

	sr.logger.Sugar().Infof("add %d file to task", len(expandedFiles))
	for _, f := range expandedFiles {
		sr.AddTask(f)
	}

	sr.CloseTaskChan()

	return sr, nil
}
