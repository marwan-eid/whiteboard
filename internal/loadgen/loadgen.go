// Package loadgen simulates editors on one board and measures sync latency:
// the time from one editor sending a batch until another editor in the same
// process receives it. Sender and receiver share this process's clock, so
// runs split across several machines stay valid without clock sync (each
// process only measures batches its own editors sent). See docs/BENCHMARKS.md.
package loadgen

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/client"
	"whiteboard/internal/hlc"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

type Config struct {
	URL     string
	BoardID string
	Editors int
	// Ramp spreads editor connects evenly over this period.
	Ramp time.Duration
	// Warmup is excluded from measurement, after the ramp.
	Warmup time.Duration
	// Duration is how long editors keep editing after the ramp.
	Duration time.Duration
	// OpsPerSec is each editor's average rate of batches, split by the shares
	// below (defaults: docs/BENCHMARKS.md scenario 1).
	OpsPerSec                                      float64
	DragShare, CreateShare, TextShare, DeleteShare float64
	// Drags come in bursts of moves at DragHz, each lasting DragMin..DragMax.
	DragHz           float64
	DragMin, DragMax time.Duration
	// CursorHz is each editor's cursor update rate.
	CursorHz float64
	// Hotspot is the fraction of editors whose view is the shared region at
	// the board's center; the rest are spread over Area.
	Hotspot float64
	Area    float64
	// ViewW and ViewH are each editor's visible area; like the web client,
	// it subscribes to that plus 50% on each side.
	ViewW, ViewH float64
	// ObjectsPerEditor is how many shapes each editor creates and then moves.
	ObjectsPerEditor int
	Seed             uint64
}

func (c *Config) setDefaults() {
	if c.Editors == 0 {
		c.Editors = 10
	}
	if c.OpsPerSec == 0 {
		c.OpsPerSec = 1
	}
	if c.Area == 0 {
		c.Area = 20_000
	}
	if c.ViewW == 0 {
		c.ViewW, c.ViewH = 1920, 1080
	}
	if c.ObjectsPerEditor == 0 {
		c.ObjectsPerEditor = 3
	}
	if c.DragShare+c.CreateShare+c.TextShare+c.DeleteShare == 0 {
		c.DragShare, c.CreateShare, c.TextShare, c.DeleteShare = 0.6, 0.2, 0.15, 0.05
	}
	if c.DragHz == 0 {
		c.DragHz = 10
	}
	if c.DragMax == 0 {
		c.DragMin, c.DragMax = time.Second, 2*time.Second
	}
}

type Result struct {
	Config    Config
	Connected int
	Failed    int
	// Late counts editors that connected only after the warmup, when latency
	// was already being measured (for example, refused by a server limit and
	// retried). A run with late editors did not have its full load.
	Late   int
	Errors map[string]int
	// Latency holds sender-to-receiver sync latency in microseconds.
	Latency *hdrhistogram.Histogram
	OpsSent int64
	Elapsed time.Duration
}

// Summary is the result in the units reported in docs/BENCHMARKS.md.
type Summary struct {
	Editors   int     `json:"editors"`
	Connected int     `json:"connected"`
	Failed    int     `json:"failed"`
	Late      int     `json:"late"`
	OpsSent   int64   `json:"ops_sent"`
	Samples   int64   `json:"latency_samples"`
	P50ms     float64 `json:"p50_ms"`
	P90ms     float64 `json:"p90_ms"`
	P99ms     float64 `json:"p99_ms"`
	P999ms    float64 `json:"p99_9_ms"`
	MaxMs     float64 `json:"max_ms"`
	ElapsedS  float64 `json:"elapsed_s"`
}

func (r Result) Summarize() Summary {
	ms := func(q float64) float64 { return float64(r.Latency.ValueAtQuantile(q)) / 1000 }
	return Summary{
		Editors: r.Config.Editors, Connected: r.Connected, Failed: r.Failed, Late: r.Late, OpsSent: r.OpsSent,
		Samples: r.Latency.TotalCount(),
		P50ms:   ms(50), P90ms: ms(90), P99ms: ms(99), P999ms: ms(99.9),
		MaxMs:    float64(r.Latency.Max()) / 1000,
		ElapsedS: r.Elapsed.Seconds(),
	}
}

func newHistogram() *hdrhistogram.Histogram {
	return hdrhistogram.New(1, int64(60*time.Second/time.Microsecond), 3)
}

// sentTimes maps a batch's stamp to when it was sent.
type sentTimes struct{ m sync.Map }

func stampKey(s hlc.Stamp) [3]uint64 {
	return [3]uint64{uint64(s.WallMs), uint64(s.Counter), s.ClientID}
}

func Run(ctx context.Context, cfg Config) Result {
	cfg.setDefaults()
	res := Result{Config: cfg, Errors: map[string]int{}, Latency: newHistogram()}
	start := time.Now()
	measureFrom := start.Add(cfg.Ramp + cfg.Warmup)
	stopAt := start.Add(cfg.Ramp + cfg.Duration)
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		sent    sentTimes
		opsSent atomic.Int64
	)
	for i := range cfg.Editors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cfg.Editors > 1 {
				select {
				case <-time.After(cfg.Ramp * time.Duration(i) / time.Duration(cfg.Editors)):
				case <-ctx.Done():
					return
				}
			}
			hist, late, err := runEditor(ctx, cfg, i, &sent, &opsSent, measureFrom, stopAt)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Failed++
				res.Errors[err.Error()]++
				return
			}
			res.Connected++
			if late {
				res.Late++
			}
			res.Latency.Merge(hist)
		}()
	}
	wg.Wait()
	res.OpsSent = opsSent.Load()
	res.Elapsed = time.Since(start)
	return res
}

func runEditor(ctx context.Context, cfg Config, i int, sent *sentTimes, opsSent *atomic.Int64, measureFrom, stopAt time.Time) (hist *hdrhistogram.Histogram, late bool, err error) {
	rng := rand.New(rand.NewPCG(cfg.Seed, uint64(i)))
	cx, cy := cfg.Area/2, cfg.Area/2
	if rng.Float64() >= cfg.Hotspot {
		cx, cy = rng.Float64()*cfg.Area, rng.Float64()*cfg.Area
	}
	view := &pb.Viewport{
		X: cx - cfg.ViewW, Y: cy - cfg.ViewH, // visible area plus 50% per side
		W: cfg.ViewW * 2, H: cfg.ViewH * 2,
	}

	// Only this editor's read goroutine touches hist.
	hist = newHistogram()
	c := client.New(client.Config{
		URL: cfg.URL, BoardID: cfg.BoardID, Reconnect: true, Viewport: view,
		OnFrame: func(f *pb.Frame) {
			now := time.Now()
			if now.Before(measureFrom) {
				return
			}
			for _, b := range f.GetBatches() {
				if t, ok := sent.m.Load(stampKey(hlc.FromProto(b.GetStamp()))); ok {
					_ = hist.RecordValue(now.Sub(t.(time.Time)).Microseconds())
				}
			}
			// A shape created (or moved) into view arrives whole; the write that
			// brought it is its newest stamp. Viewports are fixed during a run, so
			// nothing enters for any other reason.
			for _, s := range f.GetObjects() {
				var newest hlc.Stamp
				for _, fs := range s.GetStamps() {
					if st := hlc.FromProto(fs.GetStamp()); st.Compare(newest) > 0 {
						newest = st
					}
				}
				if t, ok := sent.m.Load(stampKey(newest)); ok {
					_ = hist.RecordValue(now.Sub(t.(time.Time)).Microseconds())
				}
			}
		},
	})
	defer c.Close()
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = c.Connect(dialCtx)
	cancel()
	if err != nil {
		return nil, false, fmt.Errorf("connect: %w", err)
	}
	late = time.Now().After(measureFrom)

	// Each editor starts with a few shapes near its view's center.
	e := &editor{c: c, rng: rng, cfg: &cfg, cx: cx, cy: cy, sent: sent, opsSent: opsSent}
	var ops []*pb.Op
	for range cfg.ObjectsPerEditor {
		ops = append(ops, e.newShape())
	}
	c.Edit(ops...)

	// Four Poisson processes make up the op mix (docs/BENCHMARKS.md): drag
	// bursts (each a run of moves at DragHz), creates, text edits, deletes.
	// Their rates are set so ops average OpsPerSec, split by the shares.
	burstOps := cfg.DragHz * (cfg.DragMin + cfg.DragMax).Seconds() / 2
	rate := func(share float64) float64 { return cfg.OpsPerSec * share }
	nextBurst := time.Now().Add(expWait(rng, rate(cfg.DragShare)/burstOps))
	nextCreate := time.Now().Add(expWait(rng, rate(cfg.CreateShare)))
	nextText := time.Now().Add(expWait(rng, rate(cfg.TextShare)))
	nextDelete := time.Now().Add(expWait(rng, rate(cfg.DeleteShare)))
	var drag struct {
		id          string
		x, y        float64
		next, until time.Time
	}
	var cursorEvery time.Duration
	if cfg.CursorHz > 0 {
		cursorEvery = time.Duration(float64(time.Second) / cfg.CursorHz)
	}
	nextCursor := time.Now()
	dragEvery := time.Duration(float64(time.Second) / cfg.DragHz)
	for {
		now := time.Now()
		if now.After(stopAt) || ctx.Err() != nil {
			return hist, late, nil
		}
		if cursorEvery > 0 && !now.Before(nextCursor) {
			c.MoveCursor(cx+rng.NormFloat64()*cfg.ViewW/4, cy+rng.NormFloat64()*cfg.ViewH/4)
			nextCursor = now.Add(cursorEvery)
		}
		if drag.id == "" && !now.Before(nextBurst) && len(e.own) > 0 {
			drag.id = e.own[rng.IntN(len(e.own))]
			drag.x, drag.y = cx+rng.NormFloat64()*cfg.ViewW/6, cy+rng.NormFloat64()*cfg.ViewH/6
			drag.next = now
			drag.until = now.Add(cfg.DragMin + time.Duration(rng.Int64N(int64(cfg.DragMax-cfg.DragMin)+1)))
		}
		if drag.id != "" && !now.Before(drag.next) {
			drag.x += rng.NormFloat64() * 8
			drag.y += rng.NormFloat64() * 8
			e.send(&pb.Op{Id: drag.id, Props: &pb.ObjectProps{X: proto.Float64(math.Round(drag.x)), Y: proto.Float64(math.Round(drag.y))}})
			drag.next = drag.next.Add(dragEvery)
			if drag.next.After(drag.until) {
				drag.id = ""
				nextBurst = now.Add(expWait(rng, rate(cfg.DragShare)/burstOps))
			}
		}
		if !now.Before(nextCreate) {
			e.send(e.newShape())
			nextCreate = nextCreate.Add(expWait(rng, rate(cfg.CreateShare)))
		}
		if !now.Before(nextText) {
			if len(e.own) > 0 {
				id := e.own[rng.IntN(len(e.own))]
				e.send(&pb.Op{Id: id, Props: &pb.ObjectProps{Text: proto.String(fmt.Sprintf("note %d", rng.IntN(10_000)))}})
			}
			nextText = nextText.Add(expWait(rng, rate(cfg.TextShare)))
		}
		if !now.Before(nextDelete) {
			// Keep one shape to drag; deleting the one being dragged is fine
			// (later moves edit a deleted object, as a racing user would).
			if len(e.own) > 1 {
				k := rng.IntN(len(e.own))
				id := e.own[k]
				e.own = append(e.own[:k], e.own[k+1:]...)
				e.send(&pb.Op{Id: id, Props: &pb.ObjectProps{Deleted: proto.Bool(true)}})
			}
			nextDelete = nextDelete.Add(expWait(rng, rate(cfg.DeleteShare)))
		}
		wait := min(time.Until(nextCreate), time.Until(nextText), time.Until(nextDelete))
		if drag.id != "" {
			wait = min(wait, time.Until(drag.next))
		} else {
			wait = min(wait, time.Until(nextBurst))
		}
		if cursorEvery > 0 {
			wait = min(wait, time.Until(nextCursor))
		}
		time.Sleep(max(wait, time.Millisecond))
	}
}

// editor is one simulated editor's state for sending ops.
type editor struct {
	c       *client.Client
	rng     *rand.Rand
	cfg     *Config
	cx, cy  float64
	own     []string
	sent    *sentTimes
	opsSent *atomic.Int64
}

// newShape returns a create op for a sticky note near the view's center.
func (e *editor) newShape() *pb.Op {
	id := e.c.NewObjectID()
	e.own = append(e.own, id)
	return &pb.Op{Id: id, Props: &pb.ObjectProps{
		Type: pb.ShapeType_SHAPE_TYPE_STICKY.Enum(),
		X:    proto.Float64(math.Round(e.cx + e.rng.NormFloat64()*e.cfg.ViewW/6)), Y: proto.Float64(math.Round(e.cy + e.rng.NormFloat64()*e.cfg.ViewH/6)),
		W: proto.Float64(150), H: proto.Float64(150), Fill: proto.Uint32(0xfff3b0ff), Text: proto.String("new note"),
	}}
}

// send edits and records when the batch went out, for latency.
func (e *editor) send(op *pb.Op) {
	sentAt := time.Now()
	st := e.c.Edit(op)
	e.sent.m.Store(stampKey(st), sentAt)
	e.opsSent.Add(1)
}

// expWait draws the gap to the next op of a Poisson process with this rate.
func expWait(rng *rand.Rand, rate float64) time.Duration {
	if rate <= 0 {
		return 24 * time.Hour // never, within a run
	}
	return time.Duration(rng.ExpFloat64() / rate * float64(time.Second))
}
