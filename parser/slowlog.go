package parser

import (
	"bufio"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/luijianfie/sqlreplayer/model"
)

type SlowlogParser struct {
}

func (s *SlowlogParser) Parser(filename string, pos int64, handler func(cu *model.CommandUnit) error) (int64, error) {
	var (
		cmdUnit *model.CommandUnit
		err     error
		// currentTime time.Time
		isEOF       bool
		timeRe      *regexp.Regexp
		currentTime time.Time
		timeElasped float64
		curPos      int64
	)

	curPos = pos
	nextPos := curPos

	fd, err := os.Open(filename)
	if err != nil {
		return curPos, err
	}
	defer fd.Close()

	_, err = fd.Seek(curPos, 0)

	if err != nil {
		return curPos, err
	}

	reader := bufio.NewReader(fd)

	for !isEOF {
		line, err := reader.ReadString('\n')

		if err != nil {
			if errors.Is(err, io.EOF) {
				isEOF = true
			} else {
				return curPos, err
			}
		}

		if slowlogReIgnore.MatchString(line) {
			nextPos += int64(len(line))
			continue
		}

		// if timeRe == nil {
		if slowlogTimeRe.MatchString(line) {
			timeRe = slowlogTimeRe
		} else if slowlogTimeRe56.MatchString(line) {
			timeRe = slowlogTimeRe56
		}
		// }

		if timeRe != nil {
			if m := timeRe.FindStringSubmatch(line); m != nil {

				// mysql5.6 don't save current time for each sql but only keep one timestamp every second
				if timeRe == slowlogTimeRe56 {
					currentTime, err = time.Parse("20060102 15:04:05", yearPrefix+m[1])
					//almost impossible
					if err != nil {
						return curPos, err
					}
				} else {
					currentTime, err = time.Parse("2006-01-02T15:04:05", m[1])
					//almost impossible
					if err != nil {
						return curPos, err
					}
				}
				nextPos += int64(len(line))
				continue
			}
		}

		if slowlogUserHostRe.MatchString(line) {
			if cmdUnit != nil {
				err := handler(cmdUnit)

				if err != nil {
					return curPos, err
				}
				cmdUnit = nil
				nextPos += int64(len(line))
				curPos = nextPos
			} else {
				nextPos += int64(len(line))
			}
			continue
		}

		if m := slowlogQuerytimeRe.FindStringSubmatch(line); m != nil {

			queryTime, err := strconv.ParseFloat(m[1], 64)
			if err == nil {
				timeElasped = queryTime
			}
			nextPos += int64(len(line))
			continue
		}

		if cmdUnit == nil {
			cmdUnit = &model.CommandUnit{Time: currentTime, Elapsed: timeElasped}
		}
		cmdUnit.Argument += line
		nextPos += int64(len(line))
	}

	if isEOF {
		err := handler(cmdUnit)
		if err != nil {
			return curPos, err
		}
	}

	return nextPos, nil
}
