package parser

import (
	"bufio"
	"errors"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/luijianfie/sqlreplayer/model"
)

type GeneralLogParser struct {
}

func (g *GeneralLogParser) Parser(filename string, pos int64, handler func(cu *model.CommandUnit) error) (int64, error) {

	var (
		re          *regexp.Regexp
		cmdUnit     *model.CommandUnit
		err         error
		currentTime time.Time
		isEOF       bool
		curPos      int64
	)

	curPos = pos
	fd, err := os.Open(filename)
	if err != nil {
		return curPos, err
	}
	defer fd.Close()

	_, err = fd.Seek(pos, 0)

	if err != nil {
		return curPos, err
	}

	reader := bufio.NewReader(fd)

	for !isEOF {
		line, err := reader.ReadString('\n')
		nextPos, _ := fd.Seek(0, io.SeekCurrent)

		if err != nil {
			if errors.Is(err, io.EOF) {
				isEOF = true
			} else {
				return curPos, err
			}
		}

		if reIgnore.MatchString(line) {
			curPos = nextPos
			continue
		}

		if re == nil {
			if reGenlog56.MatchString(line) {
				re = reGenlog56

			} else if reGenlog57.MatchString(line) {
				re = reGenlog57
			} else {
				curPos = nextPos
				continue
			}
		}

		if m := re.FindStringSubmatch(line); m != nil {

			// mysql5.6 don't save current time for each sql but only keep one timestamp every second
			if re == reGenlog56 {
				if m[1] != "\t" {
					currentTime, err = time.Parse("20060102 15:04:05", yearPrefix+m[1])

					//almost impossible
					if err != nil {
						return curPos, err
					}
				}
			} else {
				currentTime, err = time.Parse("2006-01-02T15:04:05", m[1])
				//almost impossible
				if err != nil {
					return curPos, err
				}
			}

			//match new sql,need to save last sql
			if cmdUnit != nil {
				err := handler(cmdUnit)

				if err != nil {
					return curPos, err
				}
				curPos = nextPos

			}

			//create new command uint
			cmdUnit = &model.CommandUnit{Time: currentTime, ThreadID: m[2], CommandType: m[3], Argument: m[4]}

		} else if cmdUnit != nil {

			//unlikely that cmdUnit is nil
			cmdUnit.Argument += line
		}

		if isEOF {
			handler(cmdUnit)
			curPos = nextPos
		}

	}

	return curPos, nil
}
