package synccore

// PlanListCap is how many paths a Plan keeps of each kind. The counts go
// on; a sync of a hundred thousand files does not hold them all in memory.
const PlanListCap = 1000

// Plan is what a sync would do, as the tool's own dry run reports it.
// Only files are counted, never directories.
type Plan struct {
	TransferCount, DeleteCount int
	Bytes                      int64    // total size of the files to transfer
	Transfers                  []string // at most PlanListCap
	Deletes                    []string // at most PlanListCap
}

// AddTransfer counts one file to transfer. The path is listed while there
// is room.
func (p *Plan) AddTransfer(path string, size int64) {
	p.TransferCount++
	p.Bytes += size
	if len(p.Transfers) < PlanListCap {
		p.Transfers = append(p.Transfers, path)
	}
}

// AddDelete counts one file to delete. The path is listed while there is
// room.
func (p *Plan) AddDelete(path string) {
	p.DeleteCount++
	if len(p.Deletes) < PlanListCap {
		p.Deletes = append(p.Deletes, path)
	}
}
