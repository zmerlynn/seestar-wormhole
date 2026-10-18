package pull

import (
	"context"
	"math"
	"sync"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
)

// Progress holds optional accounting for a command's pull. Bytes and speed come
// from rclone's actual copy/comparison readers, including local comparison reads and
// retries. Backend-native hashing is not accounted by rclone. The byte total
// grows as additional comparison operations are discovered.
// File completions use a separate stats instance: one file can cause several
// rclone transfers, but is done only after its receipt has been saved.
type Progress struct {
	io, files *accounting.StatsInfo
	// mu serializes queue metadata and snapshots, never backend I/O.
	mu          sync.Mutex
	total       int
	queuedBytes int64
}

// NewProgress isolates command accounting from prior runs. Like rclone's global
// logger and bandwidth limiter, the named group belongs to the single CLI run.
// Replacing this fixed group keeps repeated embedded command runs bounded.
func NewProgress(ctx context.Context) (context.Context, *Progress) {
	ctx = accounting.WithStatsGroup(ctx, "pull-progress")
	stats := accounting.NewStatsGroup(ctx, "pull-progress").SetMaxCompletedTransfers(0)
	return ctx, &Progress{io: stats, files: accounting.NewStats(ctx).SetMaxCompletedTransfers(0)}
}

func (p *Progress) plan(objects []fs.Object) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = len(objects)
	for _, o := range objects {
		// Inventory limits file count, but metadata sizes can still overflow a
		// display total. Saturate only the estimate; never change transfer policy.
		p.queuedBytes += min(o.Size(), math.MaxInt64-p.queuedBytes)
	}
	p.files.SetCheckQueue(p.total, 0)
	p.io.SetTransferQueue(0, p.queuedBytes)
}

func (p *Progress) start(o fs.Object) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queuedBytes = max(0, p.queuedBytes-o.Size())
	p.io.SetTransferQueue(0, p.queuedBytes)
}

func (p *Progress) complete(ctx context.Context, o fs.DirEntry) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files.NewCheckingTransfer(o, "receipt").Done(ctx, nil)
	p.files.SetCheckQueue(p.total-int(p.files.GetChecks()), 0)
}

// ProgressSnapshot contains only aggregate numeric data, never backend names or
// errors. BytesTotal includes pending listed bytes and currently known I/O work;
// it is an estimate until all copy/comparison reads have finished.
type ProgressSnapshot struct {
	FilesDone, FilesTotal int64
	BytesDone, BytesTotal int64
	Rate                  float64
}

// Snapshot reads rclone accounting without exposing its raw per-file records.
func (p *Progress) Snapshot() ProgressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, _ := p.files.RemoteStats(true)
	s, _ := p.io.RemoteStats(true)
	return ProgressSnapshot{
		FilesDone: f["checks"].(int64), FilesTotal: f["totalChecks"].(int64),
		BytesDone: s["bytes"].(int64), BytesTotal: s["totalBytes"].(int64), Rate: s["speed"].(float64),
	}
}
