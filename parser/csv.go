package parser

import (
	"encoding/csv"
	"errors"
	"io"
	"os"

	"github.com/luijianfie/sqlreplayer/model"
	"github.com/luijianfie/sqlreplayer/utils"
)

type CSVParser struct {
}

func (c *CSVParser) Parser(filename string, pos int64, handler func(cu *model.CommandUnit) error) (int64, error) {

	var (
		cmdUnit *model.CommandUnit
		curPos  int64
	)

	curPos = pos
	fd, err := os.Open(filename)
	if err != nil {
		return pos, err
	}
	defer fd.Close()

	_, err = fd.Seek(pos, 0)
	if err != nil {
		return curPos, err
	}

	reader := csv.NewReader(fd)
	reader.LazyQuotes = true

	for {
		record, err := reader.Read()
		nextPos, _ := fd.Seek(0, io.SeekCurrent)

		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return curPos, err
		}

		if record[0] == "" {
			continue
		}

		cmdUnit = &model.CommandUnit{Argument: record[0]}

		//if querid has been set, then use it , otherwise generate queryid for it
		if len(record) < 2 {
			cmdUnit.QueryID, _ = utils.GetQueryID(record[0])
		} else {
			cmdUnit.QueryID = record[1]
		}

		err = handler(cmdUnit)
		if err != nil {
			return curPos, err
		}
		curPos = nextPos

	}

	return curPos, nil
}
