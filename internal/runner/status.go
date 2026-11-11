package runner

import "tern/internal/model"

func unitOK(s model.Status) bool { return s == model.StatusSuccess || s == model.StatusWarning }

// FinalStatus derives a run's status from its units. warn says something
// outside the units went wrong without failing the run, such as the
// post-task command.
func FinalStatus(units []model.UnitResult, warn bool) model.Status {
	okCount := 0
	for _, u := range units {
		if unitOK(u.Status) {
			okCount++
			warn = warn || u.Status == model.StatusWarning
		}
	}
	switch {
	case okCount == 0:
		return model.StatusFailed
	case okCount < len(units):
		return model.StatusPartial
	case warn:
		return model.StatusWarning
	}
	return model.StatusSuccess
}
