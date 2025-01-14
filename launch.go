package sqlreplayer

import (
	"errors"

	"github.com/luijianfie/sqlreplayer/model"
	"github.com/luijianfie/sqlreplayer/utils"
)

func LanuchAnalyzeTask(jobSeq uint64, c *model.Config) (*SQLReplayer, error) {

	sr, err := NewSQLReplayer(jobSeq, c)

	if err != nil {
		return nil, err
	}

	for _, f := range c.FileList {
		if !utils.FileExists(f) {
			return nil, errors.New("file:" + f + " not exist.")
		}
	}

	for _, f := range c.FileList {
		sr.AddTask(f)
	}

	sr.CloseTaskChan()

	go sr.Start()

	return sr, nil
}
