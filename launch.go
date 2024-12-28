package sqlreplayer

import (
	"github.com/luijianfie/sqlreplayer/model"
)

func LanuchAnalyzeTask(jobSeq uint64, c *model.Config) (*SQLReplayer, error) {

	sr, err := NewSQLReplayer(jobSeq, c)

	if err != nil {
		return nil, err
	}

	go sr.Start()

	return sr, nil
}
