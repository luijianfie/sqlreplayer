package model

type ExecType int

const (
	ANALYZE ExecType = 1 << iota
	REPLAY
)

func (execType ExecType) String() string {
	switch execType {
	case ANALYZE:
		return "analyze"
	case REPLAY:
		return "replay"
	default:
		return "Unknown"
	}
}

type FileType int

const (
	GENERAL_LOG FileType = iota
	SLOW_LOG
	CSV
)

func (fileType FileType) String() string {
	switch fileType {
	case GENERAL_LOG:
		return "general_log"
	case SLOW_LOG:
		return "slow_log"
	case CSV:
		return "csv"
	default:
		return "Unknown"
	}
}
